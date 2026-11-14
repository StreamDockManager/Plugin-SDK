use serde_json::json;
use stream_dock_manager_plugin_sdk::{
    Client, Event, Handler, Result, TelemetrySnapshot, THEME_EVENT,
};
use stream_dock_manager_plugin_sdk::{HostStore, PortableSettings, RpcError, StorageMutation};
#[derive(Default)]
struct Probe {
    binding: String,
}
impl Handler for Probe {
// Include key releases alongside standard runtime events in probe logs
    fn should_log_action(&self, e: &Event) -> bool {
        e.event == "keyUp" || stream_dock_manager_plugin_sdk::should_log_action(e)
    }
// Exercise the host capabilities selected by each fixture event
    fn handle(&mut self, c: &Client, e: &Event) -> Result<()> {
        if let Some(phase) = e.settings()["storage_phase"].as_str() {
            if e.event == "willAppear" {
                self.binding = e.context.clone();
                c.title(&e.context, "storage ready")?;
            } else if e.event == "keyUp" {
                storage_probe(c, &e.context, phase)?;
                c.title(&e.context, &format!("storage {phase} passed"))?;
            }
            return Ok(());
        }
        match e.event.as_str() {
            "keyDown" if e.settings()["fixture_message_limit"] == true => {
                let prefix = "é😀";
                let base = json!({"event":"setFeedback","context":e.context,"payload":{"boundary":prefix}}).to_string().len();
                for delta in [-1isize, 0, 1] {
                    let payload = json!({"boundary":format!("{prefix}{}", "x".repeat((stream_dock_manager_plugin_sdk::MAX_MESSAGE_BYTES as isize-base as isize+delta) as usize))});
                    let result = c.send("setFeedback", &e.context, payload);
                    if delta > 0 {
                        if !result.unwrap_err().to_string().contains("1048576") {
                            return Err("missing local message-limit rejection".into());
                        }
                    } else {
                        result?;
                    }
                }
                c.feedback(&e.context, json!({"boundary":null}))?;
                c.title(&e.context, "message-limit:ok")?;
            }
            "keyDown" if e.settings()["fixture_endpoint"].is_string() => {
                let mut service = stream_dock_manager_plugin_sdk::service::Service::default();
                service.network = c.network.clone();
                let headers = std::collections::BTreeMap::from([(
                    "Authorization".into(),
                    "Bearer fixture-private".into(),
                )]);
                service.request(
                    "GET",
                    e.settings()["fixture_endpoint"].as_str().unwrap(),
                    &headers,
                    &[],
                    c.cancelled(),
                )?;
                c.log_stream("stdout", "SDK network request finished")?;
                c.title(&e.context, "network:done")?;
            }
            "willAppear" => {
                c.set_log_redactor(|text| text.replace("fixture-private", "[redacted]"));
                c.log_stream("stdout", "SDK stdout fixture-private")?;
                c.log_stream("stderr", "SDK stderr fixture-private")?;
                self.binding = e.context.clone();
                c.settings(&e.context, e.settings().clone())?;
                c.global_settings(json!({"fixture":"saved"}))?;
                c.subscribe_telemetry(&["cpu"])?
            }
            "didReceiveGlobalSettings" if e.settings()["fixture"] == "saved" => {
                c.title(&self.binding, "global:saved")?
            }
            THEME_EVENT if !self.binding.is_empty() => c.title(
                &self.binding,
                &format!("theme:{}", e.payload["resolved"].as_str().unwrap_or("")),
            )?,
            "didReceiveTelemetry" => {
                let value: TelemetrySnapshot = serde_json::from_value(e.payload.clone())?;
                if value.cpu.fresh() {
                    c.title(
                        &self.binding,
                        &format!("telemetry:{}", value.cpu.value.unwrap()),
                    )?
                }
            }
            "dialRotate" => c.title(
                &e.context,
                &format!("dial:{}", e.payload["ticks"].as_i64().unwrap_or(0)),
            )?,
            "touchTap" => c.title(&e.context, "touch")?,
            "keyUp" => {
                let request = &e
                    .surface
                    .as_ref()
                    .ok_or("missing tracked surface")?
                    .request_id;
                c.desktop_input(
                    &e.context,
                    json!({"op":"copy","request_id":format!("{request}-input")}),
                )?
            }
            "willDisappear" => self.binding.clear(),
            _ => {}
        }
        Ok(())
    }
}
// Verify private storage replay, conflicts and portable settings across restart phases
fn storage_probe(client: &Client, binding: &str, phase: &str) -> Result<()> {
    let operation = "12345678-1234-4234-8234-123456789abc";
    let expected = json!({"unicode":"é😀","unknown":{"keep":true},"false":false,"zero":0,"null":null,"empty":[],"object":{}});
    let store = HostStore::new(client.clone(), "sdk-storage-v1")?;
    let current = store.get("value")?;
    if phase == "write" {
        if current.found {
            return Err("new private value already exists".into());
        }
        let mutations = vec![
            StorageMutation::Put {
                key: "value".into(),
                value: expected.clone(),
            },
            StorageMutation::Put {
                key: "null".into(),
                value: serde_json::Value::Null,
            },
        ];
        let committed = store.commit(&current.version, operation, &mutations)?;
        let replayed = store.commit(&current.version, operation, &mutations)?;
        if replayed.version.revision != committed.version.revision {
            return Err("idempotent commit mismatch".into());
        }
        match store.commit(
            &current.version,
            "12345678-1234-4234-8234-123456789abd",
            &mutations,
        ) {
            Err(error)
                if error
                    .downcast_ref::<RpcError>()
                    .is_some_and(|value| value.code == "conflict") => {}
            _ => return Err("stale revision accepted".into()),
        }
        for scope in ["action", "global"] {
            let settings = PortableSettings::new(client.clone(), scope, binding)?;
            let before = settings
                .read()?
                .settings
                .ok_or("missing portable settings")?;
            settings.update(
                json!({"sdk_storage_probe":"saved"})
                    .as_object()
                    .unwrap()
                    .clone(),
            )?;
            let after = settings
                .read()?
                .settings
                .ok_or("missing portable settings")?;
            if before
                .iter()
                .any(|(key, value)| after.get(key) != Some(value))
            {
                return Err("portable settings lost fields".into());
            }
            if after.get("sdk_storage_probe") != Some(&json!("saved")) {
                return Err("portable update not observed".into());
            }
        }
    } else if phase == "delete" {
        if !current.found {
            return Err("private value missing before delete".into());
        }
        store.commit(
            &current.version,
            "12345678-1234-4234-8234-123456789abe",
            &[
                StorageMutation::Delete {
                    key: "value".into(),
                },
                StorageMutation::Delete { key: "null".into() },
            ],
        )?;
        if store.get("value")?.found {
            return Err("delete failed".into());
        };
        return Ok(());
    } else if phase != "read" {
        return Err("unknown storage phase".into());
    }
    let value = store.get("value")?;
    if !value.found || value.value != expected {
        return Err("private value did not round trip".into());
    }
    let null = store.get("null")?;
    if !null.found || !null.value.is_null() {
        return Err("null was treated as missing".into());
    }
    if store.get("missing")?.found {
        return Err("missing value appeared".into());
    }
    if store.list_keys(None)?.keys != ["null", "value"] {
        return Err("list keys mismatch".into());
    }
    let receipt = store.receipt(&value.version.generation, operation)?;
    if !receipt.found || !receipt.committed {
        return Err("durable receipt missing".into());
    }
    Ok(())
}
// Run the protocol probe with the host-provided launch arguments
fn main() -> Result<()> {
    let cap = stream_dock_manager_plugin_sdk::embed_capabilities!(include_str!("descriptor.txt"))?;
    stream_dock_manager_plugin_sdk::run(&mut Probe::default(), std::env::args().skip(1), Some(cap))
}
