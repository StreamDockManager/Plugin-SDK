use stream_dock_manager_plugin_sdk::{openaction_events::*, Event};

#[test]
// Verify notification parsing is explicit and preserves url
fn notification_parsing_is_explicit_and_preserves_url() {
    assert!(openaction_settings_requested(
        &Event::parse(r#"{"event":"showSettingsInterface"}"#).unwrap()
    ));
    let event = Event::parse(r#"{"event":"didReceiveDeepLink","payload":{"url":"openaction://plugins/message/example.sdPlugin/a%2Fb?openaction=hidden"}}"#).unwrap();
    assert_eq!(
        openaction_deep_link(&event).unwrap(),
        Some("openaction://plugins/message/example.sdPlugin/a%2Fb?openaction=hidden")
    );
    for payload in ["{}", r#"{"url":2}"#, r#"{"url":""}"#] {
        let event = Event::parse(&format!(
            r#"{{"event":"didReceiveDeepLink","payload":{payload}}}"#
        ))
        .unwrap();
        assert!(openaction_deep_link(&event).is_err());
    }
    assert_eq!(
        openaction_deep_link(&Event::parse(r#"{"event":"keyDown"}"#).unwrap()).unwrap(),
        None
    );
}
