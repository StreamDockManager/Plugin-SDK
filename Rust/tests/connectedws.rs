#![cfg(feature = "service-websocket")]
use serde_json::{json, Value};
use std::{
    io::{BufRead, BufReader},
    process::{Child, Command, Stdio},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    time::{Duration, Instant},
};
use stream_dock_manager_plugin_sdk::{connectedws::*, Result};
struct Peer {
    child: Child,
    endpoint: String,
}
impl Peer {
    // Initialize the instance and its owned state
    fn new(secure: bool) -> Self {
        let mut command =
            Command::new(std::env::var("STREAM_DOCK_MANAGER_NODE").unwrap_or("node".into()));
        command.arg("testdata/service-peer.mjs");
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR"));
        if std::env::var_os("STREAM_DOCK_MANAGER_WS_MODULE").is_none() {
            command.env(
                "STREAM_DOCK_MANAGER_WS_MODULE",
                root.join("../Node/node_modules/ws/wrapper.mjs"),
            );
        }
        command.env("STREAM_DOCK_MANAGER_TEST_TLS", root.join("testdata/tls"));
        if secure {
            command.arg("--tls");
        }
        let mut child = command
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()
            .unwrap();
        let output = child.stdout.take().unwrap();
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let mut line = String::new();
            BufReader::new(output).read_line(&mut line).unwrap();
            let _ = tx.send(line);
        });
        let endpoint = match rx.recv_timeout(Duration::from_secs(5)) {
            Ok(line) => line.trim().into(),
            Err(error) => {
                let _ = child.kill();
                let _ = child.wait();
                panic!("peer startup failed: {error}")
            }
        };
        Self { child, endpoint }
    }
}
impl Drop for Peer {
    // Release owned resources when the instance leaves scope
    fn drop(&mut self) {
        self.child.stdin.take();
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}
// Verify handshake
fn handshake(channel: &mut Channel) -> Result<()> {
    assert_eq!(channel.receive()?, json!({"hello":true}));
    channel.send(&json!({"auth":"fixture"}))?;
    assert_eq!(channel.receive()?, json!({"ready":true}));
    Ok(())
}
// Verify route
fn route(value: &Value) -> Result<Option<String>> {
    Ok(value["id"].as_str().map(str::to_string))
}
// Wait for the operation result or cancellation
fn wait(ready: impl Fn() -> bool) {
    let deadline = Instant::now() + Duration::from_secs(2);
    while !ready() {
        assert!(Instant::now() < deadline, "condition did not complete");
        std::thread::sleep(Duration::from_millis(1))
    }
}
#[test]
// Verify shared service sessions
fn shared_service_sessions() {
    let failure = command_error(Box::new(std::io::Error::other("lost reply")));
    assert_eq!(failure.source().unwrap().to_string(), "lost reply");

    let peer = Peer::new(false);
    let client = Arc::new(ServiceClient::default());
    let cancel = AtomicBool::new(false);
    let waiting = Arc::new(AtomicBool::new(false));
    let observed = waiting.clone();
    let (session, created) = client
        .connect(
            "same",
            &peer.endpoint,
            &cancel,
            handshake,
            move |value| {
                if value["event"] == "waiting" {
                    observed.store(true, Ordering::Release)
                }
                route(value)
            },
            || {},
        )
        .unwrap();
    assert!(created);
    assert!(client
        .connect(
            "same",
            &peer.endpoint,
            &AtomicBool::new(true),
            handshake,
            route,
            || {}
        )
        .is_err());
    assert!(client
        .connect(
            "invalid",
            "https://invalid",
            &cancel,
            handshake,
            route,
            || {}
        )
        .is_err());
    assert!(session.alive());
    assert!(
        !client
            .connect(
                "same",
                &peer.endpoint,
                &cancel,
                |_| panic!("authenticated twice"),
                route,
                || {}
            )
            .unwrap()
            .1
    );
    let mut pending = Vec::new();
    for id in ["one", "two"] {
        let session = session.clone();
        pending.push(std::thread::spawn(move || {
            session
                .request(id, &json!({"id":id}), &AtomicBool::new(false))
                .unwrap()
        }))
    }
    for (id, result) in ["one", "two"].into_iter().zip(pending) {
        assert_eq!(result.join().unwrap(), json!({"id":id}))
    }
    let stop = Arc::new(AtomicBool::new(false));
    let flag = stop.clone();
    let copy = session.clone();
    let pending = std::thread::spawn(move || copy.request("wait", &json!({"id":"wait"}), &flag));
    wait(|| waiting.load(Ordering::Acquire));
    assert!(session
        .request("wait", &json!({"id":"wait"}), &cancel)
        .is_err());
    stop.store(true, Ordering::Release);
    assert!(pending.join().unwrap().is_err());
    assert!(session.alive());
    assert!(session
        .request(
            "oversized",
            &json!({"id":"oversized","value":"x".repeat(65536)}),
            &cancel
        )
        .is_err());
    assert!(session.alive());
    assert_eq!(
        session
            .request("echo", &json!({"id":"echo"}), &cancel)
            .unwrap(),
        json!({"id":"echo"})
    );
    assert!(session
        .request("large", &json!({"id":"large"}), &cancel)
        .is_err());
    assert!(!session.alive());
    client
        .connect("again", &peer.endpoint, &cancel, handshake, route, || {})
        .unwrap();
    client.close();
    let started = Arc::new(AtomicBool::new(false));
    let begin = started.clone();
    let copy = client.clone();
    let endpoint = peer.endpoint.clone();
    let opening = std::thread::spawn(move || {
        copy.connect(
            "silent",
            &(endpoint + "/silent"),
            &AtomicBool::new(false),
            move |channel| {
                begin.store(true, Ordering::Release);
                channel.receive()?;
                Ok(())
            },
            route,
            || {},
        )
    });
    wait(|| started.load(Ordering::Acquire));
    client.close();
    assert!(opening.join().unwrap().is_err());
    assert!(
        client
            .connect("final", &peer.endpoint, &cancel, handshake, route, || {})
            .unwrap()
            .1
    );
    let (sent, received) = client.network.snapshot();
    assert!(sent > 0 && received > 0);
    assert!(client
        .connect(
            "bad",
            &(peer.endpoint.clone() + "?secret=value"),
            &cancel,
            handshake,
            route,
            || {}
        )
        .is_err());
    client.close();
    assert!(command_error(Box::new(Rejected("command".into()))).is::<Rejected>());
    assert!(command_error(std::io::Error::other("transport").into())
        .to_string()
        .contains("uncertain"));
}
#[test]
// Verify untrusted tls rejected before authentication
fn untrusted_tls_rejected_before_authentication() {
    let peer = Peer::new(true);
    let client = ServiceClient::default();
    assert!(client
        .connect(
            "tls",
            &peer.endpoint,
            &AtomicBool::new(false),
            |_| panic!("untrusted TLS reached authentication"),
            route,
            || {}
        )
        .is_err());
    client.close();
}
