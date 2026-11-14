use serde_json::{json, Value};
use stream_dock_manager_plugin_sdk::{Client, Event, HostInfo, THEME_EVENT, THEME_PROTOCOL};

#[test]
// Verify plugin theme rpc
fn plugin_theme_rpc() {
    let fixture: Value =
        serde_json::from_str(include_str!("../testdata/protocol-v1.json")).unwrap();
    let old = HostInfo::parse(&fixture["manager"].to_string()).unwrap();
    let (legacy, _) = Client::new(old, "session".into());
    assert!(legacy.get_plugin_theme().is_err());
    let host = HostInfo::parse(&fixture["theme_rpc"]["host"].to_string()).unwrap();
    let (client, outgoing) = Client::new(host, "session".into());
    std::thread::scope(|scope| {
        let result = scope.spawn(|| client.get_plugin_theme().unwrap());
        let request = outgoing
            .recv_timeout(std::time::Duration::from_secs(1))
            .unwrap();
        assert_eq!(request["protocol_id"], THEME_PROTOCOL);
        assert_eq!(request["method"], "get");
        assert_eq!(request["payload"], json!({}));
        let response = Event::parse(
            &json!({
                "event":"streamDockManagerRpcResponse", "protocol_id":THEME_PROTOCOL,
                "request_id":request["request_id"],"result":fixture["theme_rpc"]["response"],
            })
            .to_string(),
        )
        .unwrap();
        assert!(client.observe_control(&response));
        assert_eq!(result.join().unwrap().theme.surface, "#172138");
    });
    assert_eq!(client.current_plugin_theme().unwrap().revision, 1);
    assert_eq!(THEME_EVENT, "didReceivePluginTheme");
}
