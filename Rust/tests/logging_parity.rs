use base64::Engine;
#[cfg(feature = "http")]
use serde_json::json;
use serde_json::Value;
use std::collections::BTreeMap;
#[cfg(feature = "http")]
use std::sync::{Arc, Mutex};
use stream_dock_manager_plugin_sdk::*;

// Verify fixture
fn fixture() -> Value {
    serde_json::from_str(include_str!("../testdata/protocol-v1.json")).unwrap()
}

#[test]
// Verify logging completion and isolation
fn logging_completion_and_isolation() {
    struct Action(Option<Deferred>);
    impl Handler for Action {
        // Decide whether this action event belongs in diagnostic logs
        fn should_log_action(&self, event: &Event) -> bool {
            event.event == "keyUp"
        }
        // Dispatch the received event to the registered behavior
        fn handle(&mut self, client: &Client, _: &Event) -> Result<()> {
            self.0 = Some(client.defer_action());
            Ok(())
        }
    }
    let mut recorder =
        testkit::Recorder::new(HostInfo::parse(&fixture()["manager"].to_string()).unwrap());
    let mut action = Action(None);
    recorder
        .replay(
            &mut action,
            &[Event::parse(r#"{"event":"keyUp","action":"counter","context":"key"}"#).unwrap()],
        )
        .unwrap();
    assert!(recorder.drain().is_empty());
    action
        .0
        .take()
        .unwrap()
        .finish(Err(std::io::Error::other("failed").into()));
    let done = recorder.drain();
    assert_eq!(done.len(), 1);
    assert_eq!(done[0]["payload"]["stream"], "stderr");
    assert!(done[0]["payload"]["message"]
        .as_str()
        .unwrap()
        .contains("failed or cancelled"));
    for _ in 0..200 {
        let _ = recorder.client.log("flood");
    }
    recorder.client.title("key", "still responsive").unwrap();
    let values = recorder.drain();
    assert_eq!(values[0]["event"], "setTitle");
    assert_eq!(values[1]["payload"]["incomplete"], true);
    assert_eq!(values.len(), 130);
    let text = "界".repeat(12000);
    recorder.client.log(&text).unwrap();
    let frames = recorder.drain();
    assert_eq!(
        frames
            .iter()
            .map(|v| v["payload"]["message"].as_str().unwrap())
            .collect::<String>(),
        text
    );
    assert!(frames
        .iter()
        .all(|v| v["payload"]["message"].as_str().unwrap().len() <= 16384));
}

#[test]
// Verify logging network contract
fn logging_network_contract() {
    let fixture = fixture();
    let case = &fixture["logging"];
    let (client, _rx) = Client::new(
        HostInfo::parse(&fixture["manager"].to_string()).unwrap(),
        "fixture".into(),
    );
    client.isolate_logs();
    let request = &case["request"];
    let response = &case["response"];
    let headers: BTreeMap<String, String> =
        serde_json::from_value(request["headers"].clone()).unwrap();
    assert!(client
        .network
        .diagnostics
        .start("GET", request["url"].as_str().unwrap(), &headers)
        .is_none());
    struct Ignore;
    impl Handler for Ignore {
        // Dispatch the received event to the registered behavior
        fn handle(&mut self, _: &Client, _: &Event) -> Result<()> {
            Ok(())
        }
    }
    client
        .dispatch(
            &mut Ignore,
            &Event::parse(&case["preferences_event"].to_string()).unwrap(),
        )
        .unwrap();
    let mut trace = client
        .network
        .diagnostics
        .start(
            request["method"].as_str().unwrap(),
            request["url"].as_str().unwrap(),
            &headers,
        )
        .unwrap();
    trace
        .request
        .write(&serde_json::to_vec(&request["body"]).unwrap());
    trace.request.complete = true;
    trace.received(serde_json::from_value(response["headers"].clone()).unwrap());
    trace
        .response
        .write(&serde_json::to_vec(&response["body"]).unwrap());
    trace.response.complete = true;
    trace.finish(200, false);
    trace.finish(200, false);
    let values = client.drain_logs();
    assert_eq!(values.len(), 1);
    for secret in case["forbidden"].as_array().unwrap() {
        assert!(
            !values[0].to_string().contains(secret.as_str().unwrap()),
            "{secret}"
        );
    }
    let payload = &values[0]["payload"];
    assert_eq!(payload["source"], "network");
    assert_eq!(payload["stream"], "stdout");
    let body: Value =
        serde_json::from_str(payload["network"]["request"]["text"].as_str().unwrap()).unwrap();
    assert_eq!(body["value"], "retained");
    assert_eq!(payload["network"]["response"]["complete"], true);
    let mut stale = client
        .network
        .diagnostics
        .start("GET", request["url"].as_str().unwrap(), &headers)
        .unwrap();
    client.set_network_debug(false);
    client.set_network_debug(true);
    stale.finish(200, false);
    assert!(client.drain_logs().is_empty());
    let mut large = client
        .network
        .diagnostics
        .start("GET", request["url"].as_str().unwrap(), &headers)
        .unwrap();
    large.response.write(&vec![0; 65537]);
    large.finish(200, false);
    let values = client.drain_logs();
    assert_eq!(
        values[0]["payload"]["network"]["response"]["truncated"],
        true
    );
    assert_eq!(values[0]["payload"]["network"]["response"]["text"], "");
    client.set_log_redactor(|text| text.replace("application-secret", "[redacted]"));
    let mut binary = client
        .network
        .diagnostics
        .start("GET", request["url"].as_str().unwrap(), &headers)
        .unwrap();
    binary.response.write(b"\xffapplication-secret\0payload");
    binary.finish(200, false);
    let values = client.drain_logs();
    let bytes = base64::engine::general_purpose::STANDARD
        .decode(
            values[0]["payload"]["network"]["response"]["text"]
                .as_str()
                .unwrap(),
        )
        .unwrap();
    assert!(!String::from_utf8_lossy(&bytes).contains("application-secret"));
    assert!(String::from_utf8_lossy(&bytes).contains("payload"));
    let mut malformed = client
        .network
        .diagnostics
        .start("GET", request["url"].as_str().unwrap(), &headers)
        .unwrap();
    malformed.received(BTreeMap::from([(
        "Content-Type".into(),
        vec!["application/json".into()],
    )]));
    malformed
        .response
        .write(case["partial_json"].as_str().unwrap().as_bytes());
    malformed.finish(200, false);
    let values = client.drain_logs();
    assert_eq!(values[0]["payload"]["network"]["response"]["text"], "");
    assert_eq!(
        values[0]["payload"]["network"]["response"]["truncated"],
        true
    );
}

#[cfg(feature = "http")]
#[test]
// Verify http helpers emit network diagnostics
fn http_helpers_emit_network_diagnostics() {
    let history = Arc::new(Mutex::new(Vec::new()));
    let output = history.clone();
    let network = NetworkPayload::default();
    network.diagnostics.configure(
        true,
        move |value, _| output.lock().unwrap().push(value),
        str::to_string,
    );
    let peer=testkit::Peer::serve(|mut stream| {
        use std::io::{Read,Write};
        let mut data=[0;2048]; let _=stream.read(&mut data);
        stream.write_all(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 11\r\nConnection: close\r\n\r\n{\"value\":3}").unwrap();
    }).unwrap();
    let mut service = service::Service::default();
    service.network = network;
    let value = service
        .json(
            "GET",
            &format!("http://{}", peer.address),
            &BTreeMap::new(),
            None,
            Arc::new(std::sync::atomic::AtomicBool::new(false)),
        )
        .unwrap();
    assert_eq!(value, json!({"value":3}));
    assert_eq!(history.lock().unwrap().len(), 1);
}

#[test]
fn optional_network_demand() {
    struct HandlerCount(u32);
    impl Handler for HandlerCount {
        fn handle(&mut self, _: &Client, _: &Event) -> Result<()> {
            self.0 += 1;
            Ok(())
        }
    }
    let f = fixture();
    let demand = &f["network_demand"];
    for enabled in [false, true] {
        let mut host = f["manager"].clone();
        if !enabled {
            host["streamDockManager"]["capabilities"]
                .as_array_mut()
                .unwrap()
                .retain(|capability| capability != &demand["capability"]);
        }
        let mut recorder = testkit::Recorder::new(HostInfo::parse(&host.to_string()).unwrap());
        let mut handler = HandlerCount(0);
        recorder
            .client
            .dispatch(
                &mut handler,
                &Event::parse(&demand["request"].to_string()).unwrap(),
            )
            .unwrap();
        let frames = recorder.drain();
        assert_eq!(handler.0, if enabled { 0 } else { 1 });
        assert_eq!(frames.len(), usize::from(enabled));
        if enabled {
            assert_eq!(frames[0]["event"], demand["response"]);
            assert_eq!(frames[0]["payload"]["run_id"], "fixture-run");
        }
    }
}
