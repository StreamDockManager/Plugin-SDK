use serde_json::{json, Value};
use stream_dock_manager_plugin_sdk::*;
#[test]
fn permission_service_contract() {
    let f: Value = serde_json::from_str(include_str!("../testdata/permissions-v1.json")).unwrap();
    let (c,rx)=Client::new(HostInfo::parse(&json!({"streamDockManager":{"contract_version":1,"capabilities":[f["permissions"],f["broker"],f["writes"],f["references"]]}}).to_string()).unwrap(),"session".into());
    let peer = c.clone();
    let fixture = f.clone();
    let handle = std::thread::spawn(move || {
        let f = fixture;
        for (protocol, method, payload, result) in [
            (
                &f["permissions"],
                "get-permissions",
                json!({}),
                json!({"revision":f["revision"],"scopes":[]}),
            ),
            (
                &f["permissions"],
                "has-permission",
                json!({"action":f["action"],"scope":f["scope"]}),
                json!({"revision":f["revision"],"granted":false,"available":true}),
            ),
            (
                &f["permissions"],
                "request-permissions",
                json!({"activation_token":f["activation_token"],"scopes":[f["scope"]]}),
                json!({"state":"pending","revision":f["revision"],"review_id":f["review_id"]}),
            ),
            (
                &f["broker"],
                "manager-webhook",
                json!({"activation_token":f["activation_token"],"certificate":f["webhook"]["certificate"]}),
                json!({"state":"pending","result_handle":f["result_handle"]}),
            ),
            (
                &f["broker"],
                "manager-local-webhook",
                json!({"activation_token":f["activation_token"],"origin":f["webhook"]["origin"]}),
                json!({"state":"pending","result_handle":f["result_handle"]}),
            ),
            (
                &f["broker"],
                "clipboard-summary",
                json!({"activation_token":f["activation_token"]}),
                json!({"state":"pending","result_handle":f["result_handle"]}),
            ),
            (
                &f["permissions"],
                "read-clipboard-text",
                json!({"activation_token":f["activation_token"]}),
                json!({"text":f["text"],"revision":"7"}),
            ),
            (
                &f["permissions"],
                "show-text-result",
                json!({"activation_token":f["activation_token"],"text":f["text"]}),
                json!({"state":"pending","result_handle":f["result_handle"]}),
            ),
            (
                &f["permissions"],
                "notify-completion",
                json!({"activation_token":f["activation_token"]}),
                json!({"state":"completed"}),
            ),
            (
                &f["writes"],
                "write-secret",
                f["write"].clone(),
                f["receipt"].clone(),
            ),
            (
                &f["references"],
                "list-vault-entries",
                json!({"action":f["action"],"scope":f["browse"],"start":0}),
                f["page"].clone(),
            ),
            (
                &f["references"],
                "select-vault-entry",
                json!({"action":f["action"],"scope":f["use"],"activation_token":f["activation_token"]}),
                json!({"state":"pending","review_id":f["review_id"],"revision":"7"}),
            ),
            (
                &f["broker"],
                "clipboard-summary",
                json!({"scope":f["use"],"entry_ref":f["receipt"]["entry_ref"],"activation_token":f["activation_token"]}),
                json!({"state":"pending","result_handle":f["result_handle"]}),
            ),
        ] {
            let req = rx.recv().unwrap();
            assert_eq!(&req["protocol_id"], protocol);
            assert_eq!(req["method"], method);
            assert_eq!(req["payload"], payload);
            assert!(peer.observe_control(&Event::parse(&json!({"event":RPC_RESPONSE_EVENT,"protocol_id":protocol,"request_id":req["request_id"],"result":result}).to_string()).unwrap()));
        }
        let req = rx.recv().unwrap();
        assert!(peer.observe_control(&Event::parse(&json!({"event":RPC_RESPONSE_EVENT,"protocol_id":req["protocol_id"],"request_id":req["request_id"],"error":{"code":"activation_expired","message":"private fixture contents"}}).to_string()).unwrap()));
    });
    let token = f["activation_token"].as_str().unwrap();
    let opts = RequestOptions::default();
    assert_eq!(c.get_permissions(&opts).unwrap().revision, "7");
    assert!(
        !c.has_permission(f["action"].as_str().unwrap(), &f["scope"], &opts)
            .unwrap()
            .granted
    );
    assert_eq!(
        c.request_permissions(token, &[f["scope"].clone()], &opts)
            .unwrap()
            .review_id,
        f["review_id"]
    );
    assert_eq!(
        c.send_example_webhook(token, f["webhook"]["certificate"].as_str().unwrap(), &opts)
            .unwrap()
            .result_handle,
        f["result_handle"]
    );
    assert_eq!(
        c.send_local_webhook(token, f["webhook"]["origin"].as_str().unwrap(), &opts)
            .unwrap()
            .result_handle,
        f["result_handle"]
    );
    assert_eq!(
        c.summarize_clipboard(token, &opts).unwrap().result_handle,
        f["result_handle"]
    );
    assert_eq!(c.read_clipboard_text(token, &opts).unwrap().text, f["text"]);
    assert_eq!(
        c.show_text_result(token, f["text"].as_str().unwrap(), &opts)
            .unwrap()
            .result_handle,
        f["result_handle"]
    );
    assert_eq!(
        c.notify_completion(token, &opts).unwrap().state,
        "completed"
    );
    let w = &f["write"];
    assert_eq!(
        c.write_vault_secret(
            w["slot"].as_str().unwrap(),
            w["field"].as_str().unwrap(),
            f["action"].as_str().unwrap(),
            w["entry_kind"].as_str().unwrap(),
            w["value"].as_str().unwrap(),
            Some("3"),
            &opts
        )
        .unwrap()
        .entry_ref,
        f["receipt"]["entry_ref"]
    );
    let action = f["action"].as_str().unwrap();
    assert_eq!(
        c.list_vault_entries(action, &f["browse"], 0, &opts)
            .unwrap()
            .entries[0]
            .name,
        "Test place"
    );
    assert_eq!(
        c.select_vault_entry(token, action, &f["use"], &opts)
            .unwrap()
            .review_id,
        f["review_id"]
    );
    assert_eq!(
        c.run_vault_recipe(
            token,
            &f["use"],
            f["receipt"]["entry_ref"].as_str().unwrap(),
            &opts
        )
        .unwrap()
        .result_handle,
        f["result_handle"]
    );
    use stream_dock_manager_plugin_sdk::permissions::PermissionNotice;
    for key in ["terminal_event", "changed_event", "selection_event"] {
        let notice = Event::parse(&f[key].to_string())
            .unwrap()
            .permission_notice()
            .unwrap();
        match notice {
            PermissionNotice::RequestResult { review_id, .. }
            | PermissionNotice::Selection { review_id, .. } => {
                assert_eq!(review_id, f["review_id"])
            }
            PermissionNotice::Changed { scopes, .. } => {
                assert_eq!(scopes, vec![f["scope"].clone()])
            }
        }
    }
    let err = c.summarize_clipboard(token, &opts).err().unwrap();
    assert_eq!(
        err.downcast_ref::<RpcError>().unwrap().code,
        "activation_expired"
    );
    assert!(!err.to_string().contains("private fixture contents"));
    handle.join().unwrap();
    let (legacy, rx) = Client::new(HostInfo::parse("{}").unwrap(), "session".into());
    assert!(legacy.summarize_clipboard(token, &opts).is_err());
    assert!(rx.try_recv().is_err());
}

// Verify negotiation, the exact action message and redacted errors
#[test]
fn plugin_notification_contract() {
    let f: Value = serde_json::from_str(include_str!("../testdata/permissions-v1.json")).unwrap();
    let msg = &f["notification"];
    let opts = RequestOptions::default();
    let (legacy, empty) = Client::new(
        HostInfo::parse(
            &json!({"streamDockManager":{"contract_version":1,"capabilities":[f["permissions"]]}})
                .to_string(),
        )
        .unwrap(),
        "legacy".into(),
    );
    let action = msg["action"].as_str().unwrap();
    let message = msg["message"].as_str().unwrap();
    assert!(legacy
        .create_notification(action, NotificationLevel::Warning, message, &opts)
        .unwrap_err()
        .is::<UnsupportedFeatureError>());
    assert!(empty.try_recv().is_err());
    let (client, rx) = Client::new(HostInfo::parse(&json!({"streamDockManager":{"contract_version":1,"capabilities":[f["permissions"], f["notifications"]]}}).to_string()).unwrap(), "fixture".into());
    let oversized = client
        .create_notification(action, NotificationLevel::Warning, &"é".repeat(257), &opts)
        .unwrap_err();
    assert_eq!(
        oversized.downcast_ref::<RpcError>().unwrap().code,
        "too_large"
    );
    assert!(rx.try_recv().is_err());
    let peer = client.clone();
    let fixture = f.clone();
    let host = std::thread::spawn(move || {
        for reply in [
            json!({"result":fixture["notification_receipt"]}),
            json!({"result":{"state":"unexpected"}}),
            json!({"error":{"code":"permission_denied","message":"private fixture content"}}),
        ] {
            let req = rx.recv().unwrap();
            assert_eq!(req["protocol_id"], fixture["permissions"]);
            assert_eq!(req["method"], "create-notification");
            assert_eq!(req["payload"], fixture["notification"]);
            let mut response = json!({"event":RPC_RESPONSE_EVENT,"protocol_id":req["protocol_id"],"request_id":req["request_id"]});
            response
                .as_object_mut()
                .unwrap()
                .extend(reply.as_object().unwrap().clone());
            assert!(peer.observe_control(&Event::parse(&response.to_string()).unwrap()));
        }
    });
    client
        .create_notification(action, NotificationLevel::Warning, message, &opts)
        .unwrap();
    for code in ["invalid_response", "permission_denied"] {
        let error = client
            .create_notification(action, NotificationLevel::Warning, message, &opts)
            .unwrap_err();
        assert_eq!(error.downcast_ref::<RpcError>().unwrap().code, code);
        assert!(!error.to_string().contains("private fixture content"));
    }
    host.join().unwrap();
}
