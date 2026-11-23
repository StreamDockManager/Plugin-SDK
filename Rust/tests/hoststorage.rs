use serde_json::{json, Value};
use std::{
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    thread,
    time::Duration,
};
use stream_dock_manager_plugin_sdk::*;

// Verify fixture
fn fixture() -> Value {
    serde_json::from_str(include_str!("../testdata/storage-v1.json")).unwrap()
}
// Reuse or create the HTTP client for this trust identity
fn client(features: &[&str]) -> (Client, OutboundReceiver) {
    Client::new(
        HostInfo::parse(
            &json!({"streamDockManager":{"contract_version":1,"capabilities":features}})
                .to_string(),
        )
        .unwrap(),
        "opaque-session-token".into(),
    )
}
// Verify reply
fn reply(client: &Client, request: &Value, result: Value) {
    assert!(client.observe_control(&Event::parse(&json!({"event":RPC_RESPONSE_EVENT,"protocol_id":request["protocol_id"],"request_id":request["request_id"],"result":result}).to_string()).unwrap()));
}
// Build the structured failure used by the state contract
fn failure(client: &Client, request: &Value, code: &str) {
    assert!(client.observe_control(&Event::parse(&json!({"event":RPC_RESPONSE_EVENT,"protocol_id":request["protocol_id"],"request_id":request["request_id"],"error":{"code":code,"message":"test"}}).to_string()).unwrap()));
}
// Verify code
fn code(error: &Error) -> &str {
    &error
        .downcast_ref::<RpcError>()
        .expect("typed RPC failure")
        .code
}

#[test]
// Verify storage round trip preserves fixture values versions and receipts
fn storage_round_trip_preserves_fixture_values_versions_and_receipts() {
    let f = fixture();
    let (client, output) = client(&[STORAGE_PROTOCOL]);
    let host = client.clone();
    let expected = f.clone();
    let worker = thread::spawn(move || {
        for step in 0..6 {
            let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
            assert_eq!(request["event"], expected["request_event"]);
            assert_eq!(request["protocol_id"], expected["protocol_id"]);
            let id = request["request_id"].as_str().unwrap();
            assert_eq!(uuid::Uuid::parse_str(id).unwrap().to_string(), id);
            assert_eq!(request["payload"]["namespace"], expected["namespace"]);
            let mut result = json!({"generation":expected["generation"],"revision":"0"});
            match step {
                0 => {
                    assert_eq!(request["method"], "get");
                    result["found"] = json!(false);
                }
                1 => {
                    result["found"] = json!(true);
                    result["value"] = Value::Null;
                }
                2 => {
                    result["found"] = json!(true);
                    result["value"] = expected["value"].clone();
                }
                3 => {
                    assert_eq!(request["method"], "listKeys");
                    result["keys"] = json!([expected["key"]]);
                }
                4 => {
                    assert_eq!(request["method"], "commit");
                    assert_eq!(
                        request["payload"]["mutations"],
                        json!([{"op":"put","key":expected["key"],"value":expected["value"]},{"op":"delete","key":"obsolete"}])
                    );
                    assert_eq!(request["payload"]["expected_revision"], "0");
                    assert_eq!(request["payload"]["generation"], expected["generation"]);
                    result["revision"] = json!("1");
                    result["operation_id"] = expected["operation_id"].clone();
                    result["committed"] = json!(true);
                }
                _ => {
                    assert_eq!(request["method"], "receipt");
                    result["found"] = json!(false);
                    result["operation_id"] = expected["operation_id"].clone();
                    result.as_object_mut().unwrap().remove("revision");
                }
            }
            reply(&host, &request, result);
        }
    });
    let store = HostStore::new(client, f["namespace"].as_str().unwrap()).unwrap();
    let missing = store.get("missing").unwrap();
    assert!(!missing.found);
    let null = store.get("null").unwrap();
    assert!(null.found && null.value.is_null());
    assert_eq!(
        store.get(f["key"].as_str().unwrap()).unwrap().value,
        f["value"]
    );
    assert_eq!(
        store.list_keys(None).unwrap().keys,
        [f["key"].as_str().unwrap()]
    );
    let receipt = store
        .commit(
            &missing.version,
            f["operation_id"].as_str().unwrap(),
            &[
                StorageMutation::Put {
                    key: f["key"].as_str().unwrap().into(),
                    value: f["value"].clone(),
                },
                StorageMutation::Delete {
                    key: "obsolete".into(),
                },
            ],
        )
        .unwrap();
    assert!(receipt.committed);
    assert_eq!(receipt.version.revision, "1");
    assert!(
        !store
            .receipt(
                &missing.version.generation,
                f["operation_id"].as_str().unwrap()
            )
            .unwrap()
            .found
    );
    worker.join().unwrap();
}

#[test]
// Verify optional malformed metadata preserves base protocol without negotiating
fn optional_malformed_metadata_preserves_base_protocol_without_negotiating() {
    for manager in fixture()["malformed_manager"].as_array().unwrap() {
        let raw = json!({"streamDockManager":manager,"hostField":true});
        let host = HostInfo::parse(&raw.to_string()).unwrap();
        assert_eq!(host.raw, raw);
        assert!(!host.supports(STORAGE_PROTOCOL));
        let (client, output) = Client::new(host, "token".into());
        assert!(HostStore::new(client.clone(), "authoring").is_err());
        client.get_settings("context").unwrap();
        assert_eq!(
            output.recv().unwrap(),
            json!({"event":"getSettings","context":"context"})
        );
    }
    for malformed in ["null", "[]", "1", "\"host\""] {
        assert!(HostInfo::parse(malformed).is_err());
    }
}

#[test]
// Verify the explicit VSD Craft request uses the registration UUID without changing portable settings
fn vsd_craft_settings_uses_registration_uuid_and_normal_async_events() {
    let (client, output) = client(&[]);
    client.request_vsd_craft_settings().unwrap();
    assert_eq!(
        output.recv_timeout(Duration::from_secs(1)).unwrap(),
        json!({"event":"getSettings","context":"opaque-session-token"})
    );
    assert!(!client.observe_control(
        &Event::parse(&json!({"event":"didReceiveSettings","context":"binding","payload":{"settings":{"value":1}}}).to_string()).unwrap()
    ));
    client.get_settings("binding").unwrap();
    client.settings("binding", json!({"value":1})).unwrap();
    client.get_global_settings().unwrap();
    client.global_settings(json!({"value":2})).unwrap();
    for expected in [
        json!({"event":"getSettings","context":"binding"}),
        json!({"event":"setSettings","context":"binding","payload":{"value":1}}),
        json!({"event":"getGlobalSettings","context":"opaque-session-token"}),
        json!({"event":"setGlobalSettings","context":"opaque-session-token","payload":{"value":2}}),
    ] {
        assert_eq!(
            output.recv_timeout(Duration::from_secs(1)).unwrap(),
            expected
        );
    }
}

#[test]
// Reject missing registration UUIDs before sending a VSD Craft request
fn vsd_craft_settings_requires_registration_uuid() {
    for identity in ["", " 	"] {
        let (client, output) = Client::new(HostInfo::parse("{}").unwrap(), identity.into());
        assert!(client.request_vsd_craft_settings().is_err());
        assert!(output.try_recv().is_err());
    }
}

#[test]
// Verify shared base requests and merge keep unknown fields and null
fn shared_base_requests_and_merge_keep_unknown_fields_and_null() {
    let (client, output) = client(&[]);
    let host = client.clone();
    let f = fixture();
    let expected = f.clone();
    let worker = thread::spawn(move || {
        for (index, expected_request) in expected["base_requests"]
            .as_array()
            .unwrap()
            .iter()
            .enumerate()
        {
            let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
            assert_eq!(&request, expected_request);
            if index % 2 == 0 {
                let event = if index == 0 {
                    "didReceiveSettings"
                } else {
                    "didReceiveGlobalSettings"
                };
                assert!(!host.observe_control(&Event::parse(&json!({"event":event,"context":request["context"],"payload":{"settings":expected["value"]}}).to_string()).unwrap()));
            }
        }
    });
    let action = PortableSettings::new(client.clone(), "action", "owned-context").unwrap();
    assert_eq!(
        action
            .update(json!({"null":null,"zero":0}).as_object().unwrap().clone())
            .unwrap()
            .assurance,
        "sent"
    );
    let global = PortableSettings::new(client, "global", "ignored-address").unwrap();
    let read = global.read().unwrap();
    assert_eq!(read.assurance, "observed");
    assert_eq!(read.backend, "base-vsd");
    assert_eq!(
        global
            .replace(f["value"].as_object().unwrap().clone())
            .unwrap()
            .assurance,
        "sent"
    );
    worker.join().unwrap();
}

#[test]
// Verify response validation rejects bad shape version and presence
fn response_validation_rejects_bad_shape_version_and_presence() {
    let (client, output) = client(&[STORAGE_PROTOCOL]);
    let host = client.clone();
    let malformed = [
        Value::Null,
        json!({}),
        json!({"generation":"00","revision":"0","found":false}),
        json!({"generation":"1","revision":"18446744073709551616","found":false}),
        json!({"generation":"1","revision":"0","found":true}),
        json!({"generation":"1","revision":"0","found":false,"value":null}),
        json!({"generation":"1".repeat(40),"revision":"0","found":false}),
    ];
    let count = malformed.len();
    let worker = thread::spawn(move || {
        for value in malformed {
            let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
            reply(&host, &request, value);
        }
    });
    let store = HostStore::new(client, "authoring").unwrap();
    for _ in 0..count {
        assert_eq!(code(&store.get("x").unwrap_err()), "invalid_response");
    }
    worker.join().unwrap();
}

#[test]
// Verify pending budget cancellation and disconnect release waiters
fn pending_budget_cancellation_and_disconnect_release_waiters() {
    let (client, output) = client(&[STORAGE_PROTOCOL]);
    let mut workers = vec![];
    for _ in 0..8 {
        let client = client.clone();
        workers.push(thread::spawn(move || {
            client.request(STORAGE_PROTOCOL, "get", json!({}))
        }));
        output.recv_timeout(Duration::from_secs(1)).unwrap();
    }
    assert_eq!(
        code(
            &client
                .request(STORAGE_PROTOCOL, "get", json!({}))
                .unwrap_err()
        ),
        "busy"
    );
    client.stop();
    assert_eq!(
        code(
            &client
                .request(STORAGE_PROTOCOL, "get", json!({}))
                .unwrap_err()
        ),
        "cancelled_before_commit"
    );
    assert_eq!(
        code(
            &PortableSettings::new(client.clone(), "global", "")
                .unwrap()
                .read()
                .unwrap_err()
        ),
        "cancelled_before_commit"
    );
    for worker in workers {
        assert_eq!(code(&worker.join().unwrap().unwrap_err()), "indeterminate");
    }

    let (client, output) = self::client(&[STORAGE_PROTOCOL]);
    let cancelled = Arc::new(AtomicBool::new(true));
    let options = RequestOptions {
        timeout: Duration::from_secs(1),
        cancellation: Some(cancelled.clone()),
    };
    assert_eq!(
        code(
            &client
                .request_with_options(STORAGE_PROTOCOL, "get", json!({}), &options)
                .unwrap_err()
        ),
        "cancelled_before_commit"
    );
    assert!(output.try_recv().is_err());
    cancelled.store(false, Ordering::Release);
    let worker = thread::spawn(move || {
        client.request_with_options(STORAGE_PROTOCOL, "get", json!({}), &options)
    });
    output.recv_timeout(Duration::from_secs(1)).unwrap();
    cancelled.store(true, Ordering::Release);
    assert_eq!(code(&worker.join().unwrap().unwrap_err()), "indeterminate");
}

#[test]
// Verify timeout late and duplicate responses never retry or dispatch
fn timeout_late_and_duplicate_responses_never_retry_or_dispatch() {
    let (client, output) = client(&[STORAGE_PROTOCOL]);
    let result = client.request_with_options(
        STORAGE_PROTOCOL,
        "get",
        json!({}),
        &RequestOptions {
            timeout: Duration::from_millis(2),
            cancellation: None,
        },
    );
    assert_eq!(code(&result.unwrap_err()), "indeterminate");
    let request = output.recv().unwrap();
    reply(&client, &request, json!({}));
    reply(&client, &request, json!({}));
    assert!(output.try_recv().is_err());
}

#[test]
// Verify mismatched protocol ambiguous result and oversize responses fail closed
fn mismatched_protocol_ambiguous_result_and_oversize_responses_fail_closed() {
    let (client, output) = client(&[STORAGE_PROTOCOL]);
    let host = client.clone();
    let worker = thread::spawn(move || {
        for step in 0..3 {
            let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
            let mut event = json!({"event":RPC_RESPONSE_EVENT,"protocol_id":STORAGE_PROTOCOL,"request_id":request["request_id"],"result":{}});
            if step == 0 {
                event["protocol_id"] = json!("unnegotiated");
            } else if step == 1 {
                event["error"] = json!({"code":"quota","message":"test"});
            } else {
                event["result"] = json!({"oversize":"x".repeat(MAXIMUM_STORAGE_MESSAGE)});
            }
            assert!(host.observe_control(&Event::parse(&event.to_string()).unwrap()));
        }
    });
    for _ in 0..3 {
        assert_eq!(
            code(
                &client
                    .request(STORAGE_PROTOCOL, "get", json!({}))
                    .unwrap_err()
            ),
            "invalid_response"
        );
    }
    worker.join().unwrap();
}

#[test]
// Verify only explicit unsupported enhanced settings downgrades
fn only_explicit_unsupported_enhanced_settings_downgrades() {
    let (client, output) = client(&[SETTINGS_PROTOCOL]);
    let host = client.clone();
    let worker = thread::spawn(move || {
        let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
        assert_eq!(request["protocol_id"], SETTINGS_PROTOCOL);
        failure(&host, &request, "unsupported");
        let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
        assert_eq!(request, json!({"event":"getSettings","context":"owned"}));
        host.observe_control(&Event::parse(r#"{"event":"didReceiveSettings","context":"owned","payload":{"settings":{"kept":true}}}"#).unwrap());
        let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
        failure(&host, &request, "permission");
        assert!(output.recv_timeout(Duration::from_millis(30)).is_err());
    });
    let settings = PortableSettings::new(client, "action", "owned").unwrap();
    assert_eq!(settings.read().unwrap().backend, "base-vsd");
    assert_eq!(
        code(&settings.replace(Default::default()).unwrap_err()),
        "permission"
    );
    worker.join().unwrap();
}

#[test]
// Verify request limits are utf8 and fail before enqueue
fn request_limits_are_utf8_and_fail_before_enqueue() {
    let (client, output) = client(&[STORAGE_PROTOCOL]);
    assert!(HostStore::new(client.clone(), "😀".repeat(33)).is_err());
    let store = HostStore::new(client.clone(), "😀".repeat(32)).unwrap();
    assert!(store.get(&"😀".repeat(65)).is_err());
    let version = StorageVersion {
        generation: "1".into(),
        revision: "0".into(),
    };
    let id = operation_id();
    assert!(store.commit(&version, &id, &[]).is_err());
    assert!(store
        .commit(
            &version,
            &id,
            &vec![StorageMutation::Delete { key: "x".into() }; 33]
        )
        .is_err());
    assert_eq!(
        code(
            &store
                .commit(
                    &version,
                    &id,
                    &[StorageMutation::Put {
                        key: "x".into(),
                        value: json!("é".repeat(MAXIMUM_STORAGE_VALUE / 2))
                    }]
                )
                .unwrap_err()
        ),
        "quota"
    );
    assert_eq!(
        code(
            &client
                .request(
                    STORAGE_PROTOCOL,
                    "get",
                    json!({"x":"x".repeat(MAXIMUM_STORAGE_MESSAGE)})
                )
                .unwrap_err()
        ),
        "quota"
    );
    assert!(output.try_recv().is_err());
}

#[test]
// Verify shared malformed base notifications fail promptly and remain observable
fn shared_malformed_base_notifications_fail_promptly_and_remain_observable() {
    let malformed = fixture()["invalid_base_payloads"]
        .as_array()
        .unwrap()
        .clone();
    let count = malformed.len();
    let (client, output) = client(&[]);
    let host = client.clone();
    let worker = thread::spawn(move || {
        for payload in malformed {
            let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
            assert_eq!(request["event"], "getSettings");
            let notification = Event::parse(
                &json!({"event":"didReceiveSettings","context":"owned","payload":payload})
                    .to_string(),
            )
            .unwrap();
            assert!(!host.observe_control(&notification));
        }
    });
    let settings = PortableSettings::new(client, "action", "owned").unwrap();
    for _ in 0..count {
        assert_eq!(code(&settings.read().unwrap_err()), "invalid_response");
    }
    worker.join().unwrap();
}

#[test]
// Verify shared malformed rpc errors never trigger settings fallback
fn shared_malformed_rpc_errors_never_trigger_settings_fallback() {
    let malformed = fixture()["invalid_rpc_errors"].as_array().unwrap().clone();
    let count = malformed.len();
    let (client, output) = client(&[SETTINGS_PROTOCOL]);
    let host = client.clone();
    let worker = thread::spawn(move || {
        for failure in malformed {
            let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
            assert_eq!(request["event"], RPC_REQUEST_EVENT);
            assert!(host.observe_control(&Event::parse(&json!({"event":RPC_RESPONSE_EVENT,"protocol_id":SETTINGS_PROTOCOL,"request_id":request["request_id"],"error":failure}).to_string()).unwrap()));
        }
        let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
        reply(&host, &request, json!([{}, "", "observed"]));
        let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
        reply(&host, &request, json!([null, "", "committed"]));
        assert!(output.recv_timeout(Duration::from_millis(30)).is_err());
    });
    let settings = PortableSettings::new(client, "global", "").unwrap();
    for _ in 0..count {
        assert_eq!(code(&settings.read().unwrap_err()), "invalid_response");
    }
    assert_eq!(code(&settings.read().unwrap_err()), "invalid_response");
    assert_eq!(
        code(&settings.replace(Default::default()).unwrap_err()),
        "invalid_response"
    );
    worker.join().unwrap();
}

#[test]
// Verify shared absent receipts validate any supplied revision
fn shared_absent_receipts_validate_any_supplied_revision() {
    let f = fixture();
    let malformed = f["invalid_receipt_revisions"].as_array().unwrap().clone();
    let count = malformed.len();
    let (client, output) = client(&[STORAGE_PROTOCOL]);
    let host = client.clone();
    let worker = thread::spawn(move || {
        for revision in malformed {
            let request = output.recv_timeout(Duration::from_secs(1)).unwrap();
            reply(
                &host,
                &request,
                json!({"generation":request["payload"]["generation"],"revision":revision,"found":false,"operation_id":request["payload"]["operation_id"]}),
            );
        }
    });
    let store = HostStore::new(client, "authoring").unwrap();
    for _ in 0..count {
        assert_eq!(
            code(
                &store
                    .receipt(
                        f["generation"].as_str().unwrap(),
                        f["operation_id"].as_str().unwrap()
                    )
                    .unwrap_err()
            ),
            "invalid_response"
        );
    }
    worker.join().unwrap();
}
