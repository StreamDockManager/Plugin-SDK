use serde_json::{json, Value};
use std::sync::{Arc, Mutex};
use stream_dock_manager_plugin_sdk::*;
// Verify fixture
fn fixture() -> Value {
    serde_json::from_str(include_str!("../testdata/protocol-v1.json")).unwrap()
}
#[test]
// Verify shared protocol and legacy fallback
fn shared_protocol_and_legacy_fallback() {
    let f = fixture();
    let host = HostInfo::parse(&f["manager"].to_string()).unwrap();
    let legacy = HostInfo::parse(&f["legacy"].to_string()).unwrap();
    for (event, feature) in f["gates"].as_object().unwrap() {
        for h in [&host, &legacy] {
            let (c, _rx) = Client::new(h.clone(), "fixture".into());
            assert_eq!(
                c.send(event, "binding", json!({})).is_ok(),
                h.supports(feature.as_str().unwrap())
            );
        }
    }
    let device = Event::parse(&f["events"][0].to_string()).unwrap();
    assert_eq!(device.device_info().unwrap()["name"], "Fixture");
    assert_eq!(device.extra["future"], true);
    let rotation = Event::parse(&f["events"][1].to_string()).unwrap();
    assert!(rotation.pressed());
    assert_eq!(rotation.settings()["unknown"]["retain"], true);
    let (c, rx) = Client::new(legacy, "fixture".into());
    c.display(
        "binding",
        &Display {
            title: "Fallback".into(),
            state: 1,
            layout: "layout.json".into(),
            feedback: json!({"value":1}),
            ..Default::default()
        },
    )
    .unwrap();
    assert_eq!(
        rx.try_iter()
            .map(|v| v["event"].as_str().unwrap().to_string())
            .collect::<Vec<_>>(),
        ["setState", "setTitle", "setFeedbackLayout", "setFeedback"]
    );
}
#[test]
// Verify bounded queue and cancellation
fn bounded_queue_and_cancellation() {
    let (c, _rx) = Client::new(HostInfo::default(), "fixture".into());
    for _ in 0..128 {
        c.title("binding", "value").unwrap()
    }
    assert!(c.title("binding", "overflow").is_err());
    c.stop();
    assert!(c.title("binding", "late").is_err());
}
#[test]
// Verify descriptors and measurement freshness
fn descriptors_and_measurement_freshness() {
    let cap = Capabilities {
        version: 1,
        id: "org.fixture".into(),
        plugin_version: "0.1.0".into(),
        ..Default::default()
    };
    let frame = frame_capabilities(&cap).unwrap();
    assert_eq!(parse_capabilities(&frame).unwrap(), cap);
    assert!(parse_capabilities(&frame[..frame.len() - 1]).is_err());
    assert!(parse_capabilities("SDMCAP1:ffffffff:").is_err());
    let m = Measurement {
        value: Some(1.),
        observed_at: 10,
        max_age_ms: 5,
        error: None,
    };
    assert!(m.fresh_at(15));
    assert!(!m.fresh_at(16));
    assert!(!m.fresh_at(9));
    assert!(!Measurement {
        value: Some(f64::NAN),
        ..m
    }
    .fresh_at(10));
}
#[test]
// Verify deferred failure is retained and drop cancels
fn deferred_failure_is_retained_and_drop_cancels() {
    let values = Arc::new(Mutex::new(vec![]));
    let out = values.clone();
    let action = Completion::new(move |success| out.lock().unwrap().push(success));
    let first = action.defer();
    let second = action.defer();
    action.finish(Ok(()), true);
    first.finish(Ok(()));
    assert!(values.lock().unwrap().is_empty());
    drop(second);
    assert_eq!(*values.lock().unwrap(), [false]);
    action.cancel();
    assert_eq!(*values.lock().unwrap(), [false]);
}
#[test]
// Verify tracked payload counts actual reads and writes
fn tracked_payload_counts_actual_reads_and_writes() {
    use std::io::{Cursor, Read, Write};
    let counters = NetworkPayload::default();
    let mut tracked = counters.track(Cursor::new(b"abc".to_vec()));
    let mut bytes = [0; 2];
    tracked.read_exact(&mut bytes).unwrap();
    tracked.write_all(b"d").unwrap();
    assert_eq!(counters.snapshot(), (1, 2));
}
#[test]
// Verify desktop input requires matching receipt and cancels on disappearance
fn desktop_input_requires_matching_receipt_and_cancels_on_disappearance() {
    struct Input;
    impl Handler for Input {
        // Dispatch the received event to the registered behavior
        fn handle(&mut self, c: &Client, e: &Event) -> Result<()> {
            if e.event == "keyUp" {
                c.desktop_input(&e.context, json!({"request_id":"input-1","op":"copy"}))?
            }
            Ok(())
        }
    }
    let (c, rx) = Client::new(
        HostInfo::parse(&fixture()["manager"].to_string()).unwrap(),
        "fixture".into(),
    );
    let mut handler = Input;
    c.dispatch(&mut handler,&Event::parse(r#"{"event":"keyUp","context":"binding","streamDockManager":{"request_id":"action-1"}}"#).unwrap()).unwrap();
    assert_eq!(rx.try_recv().unwrap()["event"], "desktopInput");
    assert!(rx.try_recv().is_err());
    c.dispatch(&mut handler,&Event::parse(r#"{"event":"desktopInputResult","context":"foreign","payload":{"request_id":"input-1"}}"#).unwrap()).unwrap();
    assert!(rx.try_recv().is_err());
    c.dispatch(
        &mut handler,
        &Event::parse(r#"{"event":"willDisappear","context":"binding"}"#).unwrap(),
    )
    .unwrap();
    assert_eq!(
        rx.try_recv().unwrap()["payload"],
        json!({"request_id":"action-1","success":false})
    );
}

#[test]
// Verify inspector replies carry action identity
fn inspector_replies_carry_action_identity() {
    let (c, rx) = Client::new(HostInfo::default(), "fixture".into());
    c.to_inspector("binding", "org.fixture.action", json!({"value":1}))
        .unwrap();
    assert_eq!(rx.try_recv().unwrap()["action"], "org.fixture.action");
    assert!(c.to_inspector("binding", "", json!({})).is_err());
}

#[test]
// Verify typed log writers redact before framing and keep legacy fallback
fn typed_log_writers_redact_before_framing_and_keep_legacy_fallback() {
    use std::io::Write;
    for typed in [false, true] {
        let capabilities = if typed {
            vec!["plugin-logs-v1"]
        } else {
            vec![]
        };
        let host = HostInfo::parse(
            &json!({"streamDockManager":{"contract_version":1,"capabilities":capabilities}})
                .to_string(),
        )
        .unwrap();
        let (client, output) = Client::new(host, "fixture".into());
        client.set_log_redactor(|text| text.replace("secret", "[redacted]"));
        let text = format!("{}secret", "界".repeat(6000));
        assert_eq!(client.stderr().write(text.as_bytes()).unwrap(), text.len());
        let mut joined = String::new();
        for event in output.try_iter() {
            assert_eq!(event["event"], "logMessage");
            if typed {
                assert_eq!(event["payload"]["stream"], "stderr");
            } else {
                assert!(event["payload"]["stream"].is_null());
            }
            joined.push_str(event["payload"]["message"].as_str().unwrap());
        }
        assert_eq!(joined, text.replace("secret", "[redacted]"));
    }
}

#[test]
// Verify encoded message limit
fn encoded_message_limit() {
    for mode in ["manager", "legacy"] {
        let host = HostInfo::parse(&fixture()[mode].to_string()).unwrap();
        let (client, output) = Client::new(host, "fixture".into());
        for prefix in ["", "é😀", "data:image/png;base64,"] {
            let base = json!({"event":"setImage","context":"a","payload":{"image":prefix}})
                .to_string()
                .len();
            for delta in [-1isize, 0, 1] {
                let image = format!(
                    "{prefix}{}",
                    "x".repeat((MAX_MESSAGE_BYTES as isize - base as isize + delta) as usize)
                );
                let result = client.send("setImage", "a", json!({"image":image}));
                if delta > 0 {
                    assert!(result.unwrap_err().to_string().contains("1048576"));
                } else {
                    result.unwrap();
                    assert_eq!(
                        output.try_recv().unwrap().to_string().len(),
                        (MAX_MESSAGE_BYTES as isize + delta) as usize
                    );
                }
            }
        }
    }
}

#[test]
// Verify aggregate output byte budget releases and isolates logs
fn aggregate_output_byte_budget_releases_and_isolates_logs() {
    let (client, output) = Client::new(HostInfo::default(), "fixture".into());
    client.isolate_logs();
    let base = json!({"event":"setTitle","context":"a","payload":{"title":"é"}})
        .to_string()
        .len();
    let payload = json!({"title":format!("é{}", "x".repeat(MAX_MESSAGE_BYTES - base))});
    for _ in 0..8 {
        client.send("setTitle", "a", payload.clone()).unwrap();
    }
    assert!(client.send("setTitle", "a", json!({})).is_err());
    client.log_stream("stdout", "independent").unwrap();
    output.try_recv().unwrap();
    client.send("setTitle", "a", payload).unwrap();
    assert_eq!(output.try_iter().count(), 8);
    for _ in 0..128 {
        let _ = client.log_stream("stdout", &"x".repeat(16384));
    }
    client
        .send(
            "setTitle",
            "a",
            json!({"title":"receipt remains available"}),
        )
        .unwrap();
    assert!(!client.drain_logs().is_empty());
}

#[test]
// Verify shared explicit negotiation
fn shared_explicit_negotiation() {
    for case in fixture()["negotiation"].as_array().unwrap() {
        let host = HostInfo::parse(&case["host"].to_string()).unwrap();
        let supported = case["supported"].as_bool().unwrap();
        assert_eq!(
            host.supports("plugin-protocol-v1"),
            supported,
            "{}",
            case["name"]
        );
        let (client, _rx) = Client::new(host, "fixture".into());
        assert!(client
            .send("setTitle", "context", json!({"title":"base"}))
            .is_ok());
        assert!(client
            .send("setFeedback", "context", json!({"value":1}))
            .is_ok());
    }
}

#[test]
// Verify vendor inspector payload is json value
fn vendor_inspector_payload_is_json_value() {
    for payload in [
        json!(null),
        json!([1, {"unknown":true}]),
        json!("text"),
        json!(false),
        json!(0),
    ] {
        let event = Event::parse(
            &json!({"event":"sendToPlugin","context":"action","payload":payload}).to_string(),
        )
        .unwrap();
        assert_eq!(event.payload, payload);
    }
}

#[test]
// Verify required telemetry observer
fn required_telemetry_observer() {
    for case in fixture()["telemetry"].as_array().unwrap() {
        let valid = case["valid"].as_bool().unwrap();
        assert_eq!(
            serde_json::from_value::<TelemetrySnapshot>(case["payload"].clone()).is_ok(),
            valid,
            "{}",
            case["name"]
        );
        let raw = json!({"event":"didReceiveTelemetry","payload":case["payload"]});
        assert_eq!(
            Event::parse(&raw.to_string()).is_ok(),
            valid,
            "{}",
            case["name"]
        );
    }
    assert_eq!(
        serde_json::to_value(TelemetrySnapshot::default()).unwrap()["audio_observer"],
        false
    );
}
