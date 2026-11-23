use serde_json::{json, Value};
use stream_dock_manager_plugin_sdk::*;
fn peer(cap: &str, result: Value) -> (Client, std::thread::JoinHandle<()>) {
    peer_method(cap, result, false)
}
fn peer_method(cap: &str, result: Value, location: bool) -> (Client, std::thread::JoinHandle<()>) {
    let (client, rx) = Client::new(
        HostInfo::parse(
            &json!({"streamDockManager":{"contract_version":1,"capabilities":[cap]}}).to_string(),
        )
        .unwrap(),
        "opaque".into(),
    );
    let c = client.clone();
    let protocol = cap.to_string();
    let thread = std::thread::spawn(move || {
        let request: Value = rx.recv().unwrap();
        let fixture: Value =
            serde_json::from_str(include_str!("../testdata/vault-slots-v1.json")).unwrap();
        let operation = if location {
            &fixture["location"]
        } else {
            fixture["methods"]
                .as_array()
                .unwrap()
                .iter()
                .find(|entry| entry["protocol_id"] == protocol)
                .unwrap()
        };
        assert_eq!(request["event"], fixture["request_event"]);
        assert_eq!(request["protocol_id"], protocol);
        assert_eq!(request["method"], operation["method"]);
        assert_eq!(
            request["payload"],
            if location {
                &fixture["location"]["selector"]
            } else {
                &fixture["selector"]
            }
            .clone()
        );
        assert!(c.observe_control(&Event::parse(&json!({"event":RPC_RESPONSE_EVENT,"protocol_id":protocol,"request_id":request["request_id"],"result":result}).to_string()).unwrap()));
    });
    (client, thread)
}
#[test]
fn vault_read_values_and_secrets_are_exact_and_redacted() {
    for secret in [false, true] {
        let cap = if secret {
            VAULT_SECRET_SHARING_PROTOCOL
        } else {
            VAULT_VALUES_PROTOCOL
        };
        let kind = if secret { "api-key" } else { "text" };
        let (c, t) = peer(
            cap,
            json!({"entry_kind":kind,"value":"private-fixture","revision":"18446744073709551615"}),
        );
        if secret {
            let value = VaultSecrets::new(c)
                .unwrap()
                .read_secret("username", "username", "org.example.points")
                .unwrap();
            assert_eq!(value.expose_value(), "private-fixture");
            assert!(!format!("{value:?}").contains("private-fixture"));
        } else {
            let value = VaultValues::new(c)
                .unwrap()
                .read_value("username", "username", "org.example.points")
                .unwrap();
            assert_eq!(value.value, "private-fixture");
            assert!(!format!("{value:?}").contains("private-fixture"));
        }
        t.join().unwrap();
        for patch in [
            json!({"revision":"01"}),
            json!({"revision":"18446744073709551616"}),
            json!({"extra":true}),
            json!({"entry_kind":"wrong"}),
            json!({"value":"é".repeat(32769)}),
        ] {
            let mut response = json!({"entry_kind":kind,"value":"private-fixture","revision":"1"});
            response
                .as_object_mut()
                .unwrap()
                .extend(patch.as_object().unwrap().clone());
            let (c, t) = peer(cap, response);
            let err = if secret {
                VaultSecrets::new(c)
                    .unwrap()
                    .read_secret("username", "username", "org.example.points")
                    .unwrap_err()
            } else {
                VaultValues::new(c)
                    .unwrap()
                    .read_value("username", "username", "org.example.points")
                    .unwrap_err()
            };
            assert_eq!(
                err.downcast_ref::<RpcError>().unwrap().code,
                "invalid_response"
            );
            assert!(!err.to_string().contains("private-fixture"));
            t.join().unwrap();
        }
    }
}
#[test]
fn vault_requires_separate_capabilities() {
    let (c, _) = Client::new(HostInfo::parse("{}").unwrap(), "opaque".into());
    assert!(VaultValues::new(c.clone()).is_err());
    assert!(VaultSecrets::new(c).is_err());
}

#[test]
fn secret_kind_is_preserved_for_all_approved_entry_kinds() {
    for kind in ["bearer-token", "credential"] {
        let (client, thread) = peer(
            VAULT_SECRET_SHARING_PROTOCOL,
            json!({"entry_kind":kind,"value":"private-fixture","revision":"0"}),
        );
        let value = VaultSecrets::new(client)
            .unwrap()
            .read_secret("username", "username", "org.example.points")
            .unwrap();
        assert_eq!(value.entry_kind(), kind);
        assert_eq!(value.expose_value(), "private-fixture");
        thread.join().unwrap();
    }
}

#[test]
fn vault_permission_descriptor_round_trip() {
    let fixture: Value =
        serde_json::from_str(include_str!("../testdata/vault-slots-v1.json")).unwrap();
    let descriptor = Capabilities {
        permissions: Some(serde_json::from_value(fixture["permissions"].clone()).unwrap()),
        ..Default::default()
    };
    let decoded = parse_capabilities(&frame_capabilities(&descriptor).unwrap()).unwrap();
    assert_eq!(
        serde_json::to_value(decoded.permissions).unwrap(),
        fixture["permissions"]
    );
}

#[test]
fn shared_location_reads_only_coordinates() {
    let fixture: Value =
        serde_json::from_str(include_str!("../testdata/vault-slots-v1.json")).unwrap();
    let f = &fixture["location"];
    let read = |reply: Value| {
        let (c, thread) = peer_method(VAULT_VALUES_PROTOCOL, reply, true);
        let result =
            VaultValues::new(c)
                .unwrap()
                .read_location("place", "place", "org.example.points");
        thread.join().unwrap();
        result
    };
    let loc = read(f["result"].clone()).unwrap();
    assert_eq!(
        (loc.latitude, loc.longitude, loc.revision.as_str()),
        (12.34567, 0.0, "3")
    );
    assert!(!format!("{loc:?}").contains("12.34567"));
    for bad in f["invalid_coordinates"].as_array().unwrap() {
        let mut reply = f["result"].clone();
        reply["value"] = bad.clone();
        assert!(read(reply).is_err());
    }
}

#[test]
fn format_text_negotiates_only_references_and_checks_exact_result() {
    let (legacy, read) = Client::new(HostInfo::parse("{}").unwrap(), "opaque".into());
    assert_eq!(legacy.format_text("", "plain").unwrap(), "plain");
    assert_eq!(
        legacy.format_text("", "} then ${unfinished").unwrap(),
        "} then ${unfinished"
    );
    assert!(read.try_recv().is_err());
    assert!(legacy
        .format_text("example.action", "${missing}")
        .unwrap_err()
        .downcast_ref::<UnsupportedFeatureError>()
        .is_some());
    assert!(legacy.format_text("", &"é".repeat(32769)).is_err());
    for (reply, expected) in [
        (json!({"text":"Hello "}), Some("Hello ")),
        (json!({"text":null}), None),
        (json!({"text":"example","extra":true}), None),
        (json!({"text":"é".repeat(131073)}), None),
    ] {
        let (client, requests) = Client::new(
            HostInfo::parse(&json!({"streamDockManager":{"contract_version":1,"capabilities":[TEXT_REFERENCES_PROTOCOL]}}).to_string()).unwrap(),
            "opaque".into(),
        );
        let peer = client.clone();
        let server = std::thread::spawn(move || {
            let request: Value = requests.recv().unwrap();
            assert_eq!(request["protocol_id"], TEXT_REFERENCES_PROTOCOL);
            assert_eq!(request["method"], "format-text");
            assert_eq!(
                request["payload"],
                json!({"action":"example.action","text":"Hello ${missing}"})
            );
            assert!(peer.observe_control(
                &Event::parse(
                    &json!({
                        "event": RPC_RESPONSE_EVENT, "protocol_id": TEXT_REFERENCES_PROTOCOL,
                        "request_id": request["request_id"], "result":reply
                    })
                    .to_string()
                )
                .unwrap()
            ));
        });
        let actual = client.format_text("example.action", "Hello ${missing}");
        assert_eq!(actual.as_deref().ok(), expected);
        server.join().unwrap();
    }
}
