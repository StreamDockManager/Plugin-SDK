use serde_json::Value;
use std::{
    collections::BTreeMap,
    path::PathBuf,
    sync::{atomic::AtomicBool, Arc},
};
use stream_dock_manager_plugin_sdk::{buildcache, credentials, process};
#[test]
// Verify shared services contract
fn shared_services_contract() {
    let fixture: Value =
        serde_json::from_str(include_str!("../testdata/services-v1.json")).unwrap();
    for case in fixture["typed_values"].as_array().unwrap() {
        use stream_dock_manager_plugin_sdk::settings;
        assert_eq!(
            settings::number(&case["input"], "n", 99.),
            case["number"].as_f64().unwrap()
        );
        assert_eq!(
            settings::string(&case["input"], "s", "fallback"),
            case["string"].as_str().unwrap()
        );
        assert_eq!(
            settings::boolean(&case["input"], "b"),
            case["boolean"].as_bool().unwrap()
        );
    }
    for case in fixture["credentials"].as_array().unwrap() {
        let result =
            credentials::secret_with(&case["settings"], case["key"].as_str().unwrap(), |name| {
                case["lookup"][name].as_str().map(str::to_string)
            });
        if case["invalid"] == true {
            assert!(result.is_err());
        } else {
            assert_eq!(result.unwrap(), case["value"].as_str().unwrap());
        }
    }
    for case in fixture["fingerprints"].as_array().unwrap() {
        let inputs: BTreeMap<_, _> = case["inputs"]
            .as_object()
            .unwrap()
            .iter()
            .map(|(k, v)| (k.clone(), v.as_str().unwrap().as_bytes().to_vec()))
            .collect();
        assert_eq!(
            buildcache::fingerprint(&inputs, &case["configuration"]).unwrap(),
            case["expected"]
        );
    }
}
#[test]
// Verify verified cache
fn verified_cache() {
    let path = std::env::temp_dir().join(format!("sdk-cache-{}", std::process::id()));
    std::fs::write(&path, b"original").unwrap();
    buildcache::remember(&path, "key").unwrap();
    assert!(buildcache::cached(&path, "key"));
    std::fs::write(&path, b"corrupt").unwrap();
    assert!(!buildcache::cached(&path, "key"));
    std::fs::remove_file(&path).unwrap();
    std::fs::remove_file(format!("{}.build.json", path.display())).unwrap();
}

#[test]
// Verify shared cache artifacts
fn shared_cache_artifacts() {
    use std::io::{Seek, SeekFrom, Write};
    let fixture: Value =
        serde_json::from_str(include_str!("../testdata/services-v1.json")).unwrap();
    let pattern: Vec<u8> = fixture["cache_artifacts"]["byte_pattern"]
        .as_array()
        .unwrap()
        .iter()
        .map(|x| x.as_u64().unwrap() as u8)
        .collect();
    let chunk: Vec<u8> = (0..64 << 10).map(|i| pattern[i % pattern.len()]).collect();
    let path = std::env::temp_dir().join(format!("sdk-stream-cache-{}", std::process::id()));
    for case in fixture["cache_artifacts"]["cases"].as_array().unwrap() {
        let size = case["bytes"].as_u64().unwrap();
        let mut file = std::fs::File::create(&path).unwrap();
        let mut remaining = size;
        while remaining > 0 {
            let count = remaining.min(chunk.len() as u64) as usize;
            file.write_all(&chunk[..count]).unwrap();
            remaining -= count as u64;
        }
        drop(file);
        buildcache::remember(&path, "fixture").unwrap();
        assert!(buildcache::cached(&path, "fixture"));
        assert!(!buildcache::cached(&path, "different"));
        let record: Value = serde_json::from_slice(
            &std::fs::read(format!("{}.build.json", path.display())).unwrap(),
        )
        .unwrap();
        assert_eq!(record["output"], case["sha256"]);
        let mut file = std::fs::OpenOptions::new().write(true).open(&path).unwrap();
        file.seek(SeekFrom::Start(size.saturating_sub(1))).unwrap();
        file.write_all(b"c").unwrap();
        drop(file);
        assert!(!buildcache::cached(&path, "fixture"));
        std::fs::remove_file(&path).unwrap();
        assert!(!buildcache::cached(&path, "fixture"));
        assert!(buildcache::remember(&path, "fixture").is_err());
    }
    std::fs::remove_file(format!("{}.build.json", path.display())).unwrap();
}
#[test]
// Verify process fixture helper
fn process_fixture_helper() {
    println!("literal $HOME;`command` spaces and quotes Unicode 世界");
}
#[test]
// Verify bounded process and cancellation
fn bounded_process_and_cancellation() {
    let exe: PathBuf = std::env::current_exe().unwrap();
    let args = ["--exact", "process_fixture_helper", "--nocapture"];
    assert!(
        process::run(exe.to_str().unwrap(), &args, process::Options::default())
            .unwrap()
            .contains("literal $HOME;`command`")
    );
    assert!(process::run(
        exe.to_str().unwrap(),
        &args,
        process::Options {
            output_limit: 4,
            ..Default::default()
        }
    )
    .is_err());
    assert!(process::run(
        exe.to_str().unwrap(),
        &[],
        process::Options {
            cancel: Arc::new(AtomicBool::new(true)),
            ..Default::default()
        }
    )
    .is_err());
}
#[test]
// Verify connector shared reads read only inspector and teardown
fn connector_shared_reads_read_only_inspector_and_teardown() {
    use serde_json::json;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use stream_dock_manager_plugin_sdk::{
        connectors::{Choice, Connector, Driver, Options, Outcome},
        testkit::Recorder,
        Event, Handler, HostInfo, Result,
    };
    struct Fixture {
        reads: Arc<AtomicUsize>,
        commands: Arc<AtomicUsize>,
        closed: Arc<AtomicBool>,
    }
    impl Driver for Fixture {
        // Derive the identity used to share or store state
        fn key(&self, _: &Value) -> String {
            "one".into()
        }
        // Read data through the owned transport or state backend
        fn read(&mut self, _: Arc<AtomicBool>, settings: &Value) -> Result<Value> {
            assert_eq!(settings["shared"], json!({"v":[1]}));
            assert!(settings.get("missing").is_none());
            self.reads.fetch_add(1, Ordering::SeqCst);
            Ok(json!("sample"))
        }
        // Apply the requested connector command
        fn command(&mut self, _: Arc<AtomicBool>, _: &str, _: &Value, _: i32) -> Result<Outcome> {
            self.commands.fetch_add(1, Ordering::SeqCst);
            Ok(Outcome::default())
        }
        // Resolve the requested connector discovery values
        fn discover(&mut self, _: Arc<AtomicBool>, settings: &Value) -> Result<Vec<Choice>> {
            assert_eq!(settings["shared"]["v"][0], settings["expected"]);
            assert_eq!(settings.get("missing").is_some(), settings["expected"] == 3);
            Ok(vec![])
        }
        // Produce the current control display
        fn display(&self, value: &Value, _: &str, _: &Value) -> String {
            value.as_str().unwrap_or("").into()
        }
        // Close the owned session and release pending work
        fn close(&mut self) {
            self.closed.store(true, Ordering::SeqCst);
        }
    }
    let reads = Arc::new(AtomicUsize::new(0));
    let commands = Arc::new(AtomicUsize::new(0));
    let closed = Arc::new(AtomicBool::new(false));
    let mut connector = Connector::new(
        Fixture {
            reads: reads.clone(),
            commands: commands.clone(),
            closed: closed.clone(),
        },
        Options {
            interval_ms: 10000,
            shared_keys: vec!["shared".into(), "missing".into()],
            ..Default::default()
        },
    );
    connector.plugin.globals = json!({"shared":{"v":[1]}});
    connector.plugin.clock = Box::new(|| 1000);
    let mut recorder = Recorder::new(HostInfo::default());
    for id in ["a", "b"] {
        recorder.replay(&mut connector,&[Event::parse(&json!({"event":"willAppear","context":id,"action":"status","payload":{"settings":{"shared":{"v":[3]},"missing":true}}}).to_string()).unwrap()]).unwrap();
    }
    let drain = |connector: &mut Connector, recorder: &Recorder| {
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(2);
        for _ in 0..1000 {
            recorder.poll(connector).unwrap();
            if !connector.is_busy() {
                return;
            }
            assert!(std::time::Instant::now() < deadline);
            std::thread::sleep(std::time::Duration::from_millis(2));
        }
    };
    drain(&mut connector, &recorder);
    assert_eq!(reads.load(Ordering::SeqCst), 1);
    recorder
        .replay(
            &mut connector,
            &[Event::parse(
                r#"{"event":"sendToPlugin","context":"inspector","action":"status","payload":{"op":"discover","request_id":"picker-1","settings":{"shared":{"v":[3]},"missing":true,"expected":1}}}"#,
            )
            .unwrap()],
        )
        .unwrap();
    drain(&mut connector, &recorder);
    assert_eq!(commands.load(Ordering::SeqCst), 0);
    assert!(recorder
        .drain()
        .iter()
        .any(|e| e["event"] == "sendToPropertyInspector"
            && e["payload"]["request_id"] == "picker-1"
            && e["payload"]["available"] == true));
    recorder.replay(&mut connector, &[Event::parse(r#"{"event":"sendToPlugin","context":"inspector","action":"status","payload":{"op":"discover","scope":"plugin","request_id":"picker-2","settings":{"shared":{"v":[3]},"missing":true,"expected":3}}}"#).unwrap()]).unwrap();
    drain(&mut connector, &recorder);
    assert_eq!(connector.plugin.globals, json!({"shared":{"v":[1]}}));
    for id in ["a", "b"] {
        recorder
            .replay(
                &mut connector,
                &[Event::parse(
                    &json!({"event":"willDisappear","context":id,"action":"status"}).to_string(),
                )
                .unwrap()],
            )
            .unwrap();
    }
    assert!(connector.next_poll(std::time::Instant::now()).is_none());
    assert!(closed.load(Ordering::SeqCst));
    connector.close().unwrap();
}
#[test]
#[cfg(feature = "http")]
// Verify service json retry and payload accounting
fn service_json_retry_and_payload_accounting() {
    use std::io::{Read, Write};
    use stream_dock_manager_plugin_sdk::{
        service::{HTTPError, Service},
        testkit::Peer,
    };
    let peer=Peer::serve(|mut socket|{let mut request=Vec::new();let mut byte=[0];while !request.ends_with(b"\r\n\r\n"){if socket.read(&mut byte).unwrap_or(0)==0{return;}request.push(byte[0]);}let failure=String::from_utf8_lossy(&request).contains("/fail ");let bytes=if failure{b"HTTP/1.1 429 Too Many Requests\r\nRetry-After: 2\r\nContent-Length: 0\r\nConnection: close\r\n\r\n".as_slice()}else{b"HTTP/1.1 200 OK\r\nContent-Length: 12\r\nConnection: close\r\n\r\n{\"value\":42}".as_slice()};socket.write_all(bytes).unwrap();}).unwrap();
    let service = Service::default();
    let url = format!("http://{}", peer.address);
    let headers = BTreeMap::new();
    let cancel = Arc::new(AtomicBool::new(false));
    assert_eq!(
        service
            .json("GET", &url, &headers, None, cancel.clone())
            .unwrap(),
        serde_json::json!({"value":42})
    );
    let failure = service
        .json("GET", &(url + "/fail"), &headers, None, cancel)
        .unwrap_err();
    let failure = failure.downcast_ref::<HTTPError>().unwrap();
    assert_eq!(failure.status, 429);
    assert!(failure.retry_at.is_some());
    assert_eq!(service.network.snapshot().1, 12);
}
#[test]
#[cfg(feature = "testkit-tls")]
// Verify disposable tls peer uses only test roots
fn disposable_tls_peer_uses_only_test_roots() {
    use std::io::{Read, Write};
    use stream_dock_manager_plugin_sdk::testkit::{tls, Peer};
    let (server, client) = tls().unwrap();
    let peer = Peer::serve(move |socket| {
        let connection = rustls::ServerConnection::new(server.clone()).unwrap();
        let mut stream = rustls::StreamOwned::new(connection, socket);
        let mut data = [0; 4];
        stream.read_exact(&mut data).unwrap();
        stream.write_all(&data).unwrap();
        stream.flush().unwrap();
    })
    .unwrap();
    let connection = rustls::ClientConnection::new(
        client,
        rustls::pki_types::ServerName::try_from("localhost")
            .unwrap()
            .to_owned(),
    )
    .unwrap();
    let socket = std::net::TcpStream::connect(peer.address).unwrap();
    socket
        .set_read_timeout(Some(std::time::Duration::from_secs(2)))
        .unwrap();
    socket
        .set_write_timeout(Some(std::time::Duration::from_secs(2)))
        .unwrap();
    let mut stream = rustls::StreamOwned::new(connection, socket);
    stream.write_all(b"ping").unwrap();
    stream.flush().unwrap();
    let mut data = [0; 4];
    stream.read_exact(&mut data).unwrap();
    assert_eq!(&data, b"ping");
}

#[test]
// Verify connector commands are serialized and not replayed
fn connector_commands_are_serialized_and_not_replayed() {
    use serde_json::json;
    use std::sync::{
        atomic::{AtomicUsize, Ordering},
        Mutex,
    };
    use stream_dock_manager_plugin_sdk::{connectors::*, testkit::Recorder, Event, HostInfo};
    struct Fixture {
        commands: Arc<Mutex<Vec<i32>>>,
        active: Arc<AtomicUsize>,
        peak: Arc<AtomicUsize>,
    }
    impl Driver for Fixture {
        // Derive the identity used to share or store state
        fn key(&self, _: &Value) -> String {
            "one".into()
        }
        // Read data through the owned transport or state backend
        fn read(
            &mut self,
            _: Arc<AtomicBool>,
            _: &Value,
        ) -> stream_dock_manager_plugin_sdk::Result<Value> {
            Ok(json!("sample"))
        }
        // Apply the requested connector command
        fn command(
            &mut self,
            _: Arc<AtomicBool>,
            _: &str,
            _: &Value,
            ticks: i32,
        ) -> stream_dock_manager_plugin_sdk::Result<Outcome> {
            let active = self.active.fetch_add(1, Ordering::SeqCst) + 1;
            self.peak.fetch_max(active, Ordering::SeqCst);
            self.commands.lock().unwrap().push(ticks);
            std::thread::sleep(std::time::Duration::from_millis(5));
            self.active.fetch_sub(1, Ordering::SeqCst);
            Ok(Outcome::default())
        }
        // Resolve the requested connector discovery values
        fn discover(
            &mut self,
            _: Arc<AtomicBool>,
            _: &Value,
        ) -> stream_dock_manager_plugin_sdk::Result<Vec<Choice>> {
            Ok(vec![])
        }
        // Produce the current control display
        fn display(&self, _: &Value, _: &str, _: &Value) -> String {
            "sample".into()
        }
        // Close the owned session and release pending work
        fn close(&mut self) {}
    }
    let commands = Arc::new(Mutex::new(vec![]));
    let peak = Arc::new(AtomicUsize::new(0));
    let mut connector = Connector::new(
        Fixture {
            commands: commands.clone(),
            active: Arc::new(AtomicUsize::new(0)),
            peak: peak.clone(),
        },
        Options {
            dial_actions: vec!["volume".into()],
            interval_ms: 10000,
            ..Default::default()
        },
    );
    let recorder = Recorder::new(HostInfo::default());
    let event = |value: Value| Event::parse(&value.to_string()).unwrap();
    recorder.replay(&mut connector,&[event(json!({"event":"willAppear","context":"a","action":"volume","payload":{"settings":{}}}))]).unwrap();
    for tick in [2, 3, -1, 4] {
        recorder.replay(&mut connector,&[event(json!({"event":"dialRotate","context":"a","action":"volume","payload":{"ticks":tick}}))]).unwrap();
    }
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(2);
    while commands.lock().unwrap().len() < 4 || connector.is_busy() {
        recorder.poll(&mut connector).unwrap();
        assert!(std::time::Instant::now() < deadline);
        std::thread::sleep(std::time::Duration::from_millis(2));
    }
    for _ in 0..5 {
        recorder.poll(&mut connector).unwrap();
    }
    assert_eq!(*commands.lock().unwrap(), vec![2, 3, -1, 4]);
    assert_eq!(peak.load(Ordering::SeqCst), 1);
    recorder
        .replay(
            &mut connector,
            &[event(
                json!({"event":"willDisappear","context":"a","action":"volume"}),
            )],
        )
        .unwrap();
}
