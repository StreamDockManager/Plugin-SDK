use serde_json::{json, Value};
use std::{
    collections::{BTreeMap, BTreeSet},
    path::PathBuf,
    sync::{atomic::AtomicU64, atomic::Ordering},
    time::{SystemTime, UNIX_EPOCH},
};
use stream_dock_manager_plugin_sdk::{
    authoring::*, httpclient, polling, settings, storage, testkit, Event, HostInfo,
};
// Verify fixture
fn fixture() -> Value {
    serde_json::from_str(include_str!("../testdata/authoring-v1.json")).unwrap()
}
#[test]
// Verify shared fields display paths redaction retries
fn shared_fields_display_paths_redaction_retries() {
    let f = fixture();
    let fields: Vec<settings::Field> = serde_json::from_value(f["fields"].clone()).unwrap();
    for case in f["settings"].as_array().unwrap() {
        let result = settings::validate(&fields, &case["input"]);
        assert_eq!(result.is_ok(), case["valid"].as_bool().unwrap());
        if let Ok(value) = result {
            assert_eq!(value, case["expected"])
        }
    }
    for schema in f["invalid_schemas"].as_array().unwrap() {
        let fields: Vec<settings::Field> = serde_json::from_value(schema.clone()).unwrap();
        assert!(settings::validate(&fields, &json!({})).is_err());
    }
    for case in f["displays"].as_array().unwrap() {
        let prior: Option<Display> = serde_json::from_value(case["previous"].clone()).unwrap();
        let desired: Display = serde_json::from_value(case["desired"].clone()).unwrap();
        assert_eq!(
            json!(display_messages(
                "a",
                prior.as_ref(),
                &desired,
                case["rich"].as_bool().unwrap(),
                true
            )),
            case["expected"],
            "{}",
            case["name"]
        )
    }
    for case in f["retries"].as_array().unwrap() {
        assert_eq!(
            polling::retry_delay(
                case["interval_ms"].as_u64().unwrap(),
                case["failures"].as_u64().unwrap() as u32,
                case["jitter"].as_f64().unwrap()
            ),
            case["expected_ms"].as_u64().unwrap()
        )
    }
    for case in f["paths"].as_array().unwrap() {
        let value =
            httpclient::extract_path(&case["root"], case["path"].as_str().unwrap()).unwrap();
        assert_eq!(value.is_some(), case["found"].as_bool().unwrap());
        if let Some(value) = value {
            assert_eq!(value, case["value"])
        }
    }
    for path in f["invalid_paths"].as_array().unwrap() {
        assert!(httpclient::parse_path(path.as_str().unwrap()).is_err())
    }
    let redaction = &f["redaction"];
    assert_eq!(
        httpclient::redact(
            &redaction["input"],
            &serde_json::from_value::<Vec<String>>(redaction["secrets"].clone()).unwrap()
        ),
        redaction["expected"]
    );
}
static TEMP: AtomicU64 = AtomicU64::new(0);
// Verify temporary
fn temporary() -> PathBuf {
    let path = std::env::temp_dir().join(format!(
        "sdk-state-{}-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos(),
        TEMP.fetch_add(1, Ordering::Relaxed)
    ));
    std::fs::create_dir_all(&path).unwrap();
    path
}
#[test]
// Verify shared state writes preserve exact format and failed write
fn shared_state_writes_preserve_exact_format_and_failed_write() {
    for _ in 0..5 {
        let root = temporary();
        for case in fixture()["state_writes"]["cases"].as_array().unwrap() {
            let path = root.join("state.json");
            let version = case["version"].as_u64().unwrap() as u32;
            let value = case["value"].clone();
            let expected = if version == 0 {
                value.clone()
            } else {
                json!({"version":version, "data":value})
            };
            let expected = serde_json::to_vec(&expected).unwrap();
            let mut store = storage::Store::new(&path, version);
            store.change(value.clone(), 0);
            store.close().unwrap();
            assert_eq!(std::fs::read(&path).unwrap(), expected);
            assert_eq!(
                storage::Store::new(&path, version).load().unwrap(),
                Some(value)
            );
            let backup = root.join("backup.json");
            std::fs::rename(&path, &backup).unwrap();
            std::fs::create_dir(&path).unwrap();
            store.change(json!({"changed":true}), 0);
            assert!(store.close().is_err());
            assert!(store.dirty);
            assert_eq!(std::fs::read(&backup).unwrap(), expected);
            std::fs::remove_dir(&path).unwrap();
            std::fs::rename(&backup, &path).unwrap();
            store.close().unwrap();
            assert_eq!(
                storage::Store::new(&path, version).load().unwrap(),
                Some(json!({"changed":true}))
            );
        }
        std::fs::remove_dir_all(root).unwrap();
    }
}
#[test]
// Verify shared state missing paths distinguish absent and blocked ancestors
fn shared_state_missing_paths_distinguish_absent_and_blocked_ancestors() {
    let root = temporary();
    std::fs::write(root.join("blocked"), b"keep").unwrap();
    for case in fixture()["state_reads"]["missing_paths"]
        .as_array()
        .unwrap()
    {
        let path = root.join(case["path"].as_str().unwrap());
        let mut store = storage::Store::new(path, 1);
        store.change(json!({"previous":true}), 0);
        let loaded = store.load();
        if case["error"].as_bool().unwrap() {
            assert!(loaded.is_err(), "{}", case["name"]);
        } else {
            assert_eq!(loaded.unwrap(), None, "{}", case["name"]);
        }
        assert_eq!(store.value, json!({"previous":true}));
        assert!(store.dirty);
        assert_eq!(store.due, Some(250));
        assert_eq!(std::fs::read(root.join("blocked")).unwrap(), b"keep");
    }
    std::fs::remove_dir_all(root).unwrap();
}

// Verify directory link
fn directory_link(target: &std::path::Path, link: &std::path::Path) {
    #[cfg(unix)]
    {
        std::os::unix::fs::symlink(target, link).expect("directory symlink fixture unavailable");
        eprintln!("directory alias fixture: symlink");
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        use std::process::{Command, Stdio};
        use std::time::{Duration, Instant};
        if std::os::windows::fs::symlink_dir(target, link).is_ok() {
            eprintln!("directory alias fixture: symlink");
            return;
        }
        // Both endpoints are owned by this test. Never permit cmd expansion
        let target = target.to_str().expect("UTF-8 fixture target");
        let link = link.to_str().expect("UTF-8 fixture alias");
        assert!(
            !target
                .chars()
                .chain(link.chars())
                .any(|c| "%!&|<>^\"()\r\n".contains(c)),
            "junction fixture cannot safely quote this temporary directory"
        );
        let mut child = Command::new("cmd.exe")
            .args(["/D", "/V:OFF", "/C"])
            .raw_arg(format!("mklink /J \"{link}\" \"{target}\""))
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("directory symlink unavailable and junction helper could not start");
        let deadline = Instant::now() + Duration::from_secs(10);
        loop {
            if let Some(status) = child.try_wait().expect("junction helper status") {
                assert!(
                    status.success(),
                    "directory junction fixture failed: {status}"
                );
                break;
            }
            if Instant::now() >= deadline {
                let _ = child.kill();
                let _ = child.wait();
                panic!("directory junction fixture timed out after 10 seconds");
            }
            std::thread::sleep(Duration::from_millis(10));
        }
        eprintln!("directory alias fixture: Windows junction (symlink privilege unavailable)");
    }
}

#[test]
// Verify missing state allows directory symlinks and rejects dangling links
fn missing_state_allows_directory_symlinks_and_rejects_dangling_links() {
    let root = temporary();
    std::fs::create_dir(root.join("directory")).unwrap();
    std::fs::create_dir(root.join("missing")).unwrap();
    directory_link(&root.join("directory"), &root.join("valid"));
    directory_link(&root.join("missing"), &root.join("dangling"));
    std::fs::remove_dir(root.join("missing")).unwrap();
    for path in [
        "dangling",
        "dangling/state.json",
        "dangling/nested/state.json",
    ] {
        assert!(storage::Store::new(root.join(path), 1).load().is_err());
    }
    let mut store = storage::Store::new(root.join("valid/nested/state.json"), 1);
    assert_eq!(store.load().unwrap(), None);
    store.change(json!({"saved":true}), 0);
    store.close().unwrap();
    assert_eq!(
        std::fs::read(root.join("directory/nested/state.json")).unwrap(),
        br#"{"data":{"saved":true},"version":1}"#
    );
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
// Verify shared state read limits
fn shared_state_read_limits() {
    use std::io::Write;
    let contract = &fixture()["state_reads"];
    assert_eq!(
        storage::MAXIMUM_BYTES as u64,
        contract["maximum_bytes"].as_u64().unwrap()
    );
    let root = temporary();
    let path = root.join("state.json");
    for case in contract["cases"].as_array().unwrap() {
        let version = case["version"].as_u64().unwrap() as u32;
        let value = if version == 0 {
            contract["payload"].clone()
        } else {
            json!({"version":version, "data":contract["payload"]})
        };
        let raw = serde_json::to_vec(&value).unwrap();
        let size = storage::MAXIMUM_BYTES + case["extra_bytes"].as_u64().unwrap() as usize;
        let mut file = std::fs::File::create(&path).unwrap();
        file.write_all(&raw).unwrap();
        let padding = [b' '; 64 << 10];
        let mut remaining = size - raw.len();
        while remaining > 0 {
            let chunk = remaining.min(padding.len());
            file.write_all(&padding[..chunk]).unwrap();
            remaining -= chunk;
        }
        drop(file);
        let mut store = storage::Store::new(&path, version);
        store.value = json!({"previous":true});
        let result = store.load();
        if case["valid"].as_bool().unwrap() {
            assert_eq!(
                result.unwrap(),
                Some(contract["payload"].clone()),
                "{}",
                case["name"]
            );
        } else {
            assert!(result.unwrap_err().to_string().contains("exceeds"));
            assert_eq!(store.value, json!({"previous":true}));
            assert!(!store.dirty);
        }
        assert_eq!(std::fs::metadata(&path).unwrap().len(), size as u64);
        if !case["valid"].as_bool().unwrap() {
            store.change(json!({"replacement":true}), 0);
            assert!(store
                .close()
                .unwrap_err()
                .to_string()
                .contains("saved state is protected"));
            assert!(store.dirty);
            assert_eq!(std::fs::metadata(&path).unwrap().len(), size as u64);
            let backup = path.with_extension("saved");
            if case["name"] == "large_file" {
                std::fs::rename(&path, &backup).unwrap();
            } else {
                std::fs::write(&path, &raw).unwrap();
            }
            let restored = store.load().unwrap();
            if case["name"] != "large_file" {
                assert_eq!(restored, Some(contract["payload"].clone()));
            }
            store.change(contract["payload"].clone(), 0);
            store.close().unwrap();
            assert_eq!(
                storage::Store::new(&path, version).load().unwrap(),
                Some(contract["payload"].clone())
            );
            if case["name"] == "large_file" {
                assert_eq!(std::fs::metadata(&backup).unwrap().len(), size as u64);
            }
        }
    }
    std::fs::remove_dir_all(root).unwrap();
}
#[test]
// Verify state debounce rejection and failed write retention
fn state_debounce_rejection_and_failed_write_retention() {
    let root = temporary();
    let path = root.join("state.json");
    let mut store = storage::Store::new(&path, 1);
    store.change(json!({"count":1}), 0);
    assert!(!store.flush(249, false).unwrap());
    assert!(!path.exists());
    assert!(store.flush(250, false).unwrap());
    assert_eq!(
        storage::Store::new(&path, 1).load().unwrap(),
        Some(json!({"count":1}))
    );
    storage::write(&path, &json!({"legacy":true})).unwrap();
    assert!(storage::Store::new(&path, 1).load().is_err());
    let mut rejected = storage::Store::new(&path, 1);
    assert!(rejected.load().is_err());
    rejected.change(json!({}), 0);
    assert!(rejected.close().is_err());
    let mut bad = storage::Store::new(path.join("child"), 1);
    bad.change(json!({}), 0);
    assert!(bad.close().is_err());
    assert!(bad.dirty);
    std::fs::remove_dir_all(root).unwrap();
}
#[test]
// Verify resources share work and reject retired generations
fn resources_share_work_and_reject_retired_generations() {
    let mut resources = polling::Resources::default();
    resources.watch("a", "resource", 1000).unwrap();
    resources.watch("b", "resource", 2000).unwrap();
    assert_eq!(resources.due(0), ["resource"]);
    let generation = resources.begin("resource").unwrap();
    assert!(resources.due(0).is_empty());
    resources.unwatch("a");
    resources.unwatch("b");
    resources.watch("b", "resource", 1000).unwrap();
    let replacement = resources.begin("resource").unwrap();
    assert_ne!(generation, replacement);
    assert!(!resources.complete(
        "resource",
        generation,
        polling::PollResult {
            value: json!(42),
            ..Default::default()
        }
    ));
    assert!(resources.complete(
        "resource",
        replacement,
        polling::PollResult {
            value: json!(42),
            ..Default::default()
        }
    ));
}
#[test]
// Verify view relationships require matching identity
fn view_relationships_require_matching_identity() {
    let mut bindings = BTreeMap::new();
    for (context, action, shared) in [
        ("a", "start", "one"),
        ("copy", "start", "two"),
        ("view", "status", "one"),
    ] {
        bindings.insert(
            context.into(),
            Binding {
                action: action.into(),
                settings: json!({"shared_id":shared}),
                surface: None,
            },
        );
    }
    let mut targets = BTreeMap::new();
    targets.insert(
        "status".into(),
        ViewTarget {
            action: "status".into(),
            updated_by: vec!["start".into()],
            shared: true,
            live: true,
            ..Default::default()
        },
    );
    let mut scope = Scope {
        origin: "a".into(),
        action: "start".into(),
        ..Default::default()
    };
    assert!(can_update(
        &bindings,
        &targets,
        &BTreeSet::new(),
        "view",
        &scope,
        &json!({})
    ));
    scope.origin = "copy".into();
    assert!(!can_update(
        &bindings,
        &targets,
        &BTreeSet::new(),
        "view",
        &scope,
        &json!({})
    ));
}
#[test]
// Verify testkit dispatches real lifecycle
fn testkit_dispatches_real_lifecycle() {
    struct Counter;
    impl Action for Counter {
        // Produce the current display representation
        fn render(
            &mut self,
            _: &mut Plugin,
            _: &str,
            _: &Binding,
        ) -> stream_dock_manager_plugin_sdk::Result<Option<Display>> {
            Ok(Some(Display {
                title: "Ready".into(),
                ..Default::default()
            }))
        }
    }
    let mut recorder = testkit::Recorder::new(HostInfo::default());
    let mut plugin = Plugin::default();
    plugin.register("org.fixture.action", Counter).unwrap();
    recorder.replay(&mut plugin,&[Event::parse(r#"{"event":"willAppear","context":"a","action":"org.fixture.action","payload":{"settings":{"opaque":true}}}"#).unwrap()]).unwrap();
    assert!(!recorder.drain().is_empty());
    recorder
        .replay(
            &mut plugin,
            &[Event::parse(r#"{"event":"willDisappear","context":"a"}"#).unwrap()],
        )
        .unwrap();
    assert!(plugin.bindings.is_empty());
}

#[test]
// Verify touch theme and fake clock store follow shared lifecycle
fn touch_theme_and_fake_clock_store_follow_shared_lifecycle() {
    use stream_dock_manager_plugin_sdk::Handler;
    let root = temporary();
    let clock = testkit::Clock::new(0);
    let mut plugin = Plugin::default();
    let reader = clock.clone();
    plugin.clock = Box::new(move || reader.now());
    plugin.store = Some(storage::Store::new(root.join("state.json"), 1));
    plugin
        .store
        .as_mut()
        .unwrap()
        .change(json!({"count":1}), clock.now());
    let recorder = testkit::Recorder::new(HostInfo::default());
    for (phase, expected) in [("start", false), ("hold", false), ("end", true)] {
        recorder.replay(&mut plugin, &[Event::parse(&json!({"event":"touchTap","context":"a","action":"org.fixture.action","streamDockManager":{"touch":{"phase":phase}}}).to_string()).unwrap()]).unwrap();
        assert_eq!(plugin.active.contains("a"), expected);
    }
    recorder.poll(&mut plugin).unwrap();
    assert!(!root.join("state.json").exists());
    clock.advance(250);
    recorder.poll(&mut plugin).unwrap();
    assert!(root.join("state.json").exists());
    plugin.renderer.last.insert(
        "a".into(),
        Display {
            title: "literal-dark.svg".into(),
            image: "icon-dark.svg".into(),
            ..Default::default()
        },
    );
    plugin
        .handle(
            &recorder.client,
            &Event::parse(r#"{"event":"didReceivePluginTheme","payload":{"resolved":"light"}}"#)
                .unwrap(),
        )
        .unwrap();
    assert_eq!(plugin.renderer.last["a"].title, "literal-dark.svg");
    assert_eq!(plugin.renderer.last["a"].image, "icon-light.svg");
    plugin.close().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
// Verify keyed work application failure completes on loop
fn keyed_work_application_failure_completes_on_loop() {
    use stream_dock_manager_plugin_sdk::Handler;
    struct Start;
    impl Action for Start {
        // Dispatch the received event to the registered behavior
        fn handle(
            &mut self,
            plugin: &mut Plugin,
            client: &stream_dock_manager_plugin_sdk::Client,
            event: &Event,
        ) -> stream_dock_manager_plugin_sdk::Result<()> {
            if event.event == "keyUp" {
                plugin.background(
                    client,
                    "job",
                    &event.context,
                    false,
                    |_| Ok(json!(42)),
                    |_, _, _| Err(std::io::Error::other("application failed").into()),
                )?;
            }
            Ok(())
        }
    }
    let fixture: Value =
        serde_json::from_str(include_str!("../testdata/protocol-v1.json")).unwrap();
    let mut recorder =
        testkit::Recorder::new(HostInfo::parse(&fixture["manager"].to_string()).unwrap());
    let mut plugin = Plugin::default();
    plugin.register("org.fixture.action", Start).unwrap();
    recorder.replay(&mut plugin,&[Event::parse(r#"{"event":"keyUp","context":"a","action":"org.fixture.action","streamDockManager":{"request_id":"request-1"}}"#).unwrap()]).unwrap();
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(2);
    while std::time::Instant::now() < deadline {
        recorder.poll(&mut plugin).unwrap();
        if recorder
            .drain()
            .iter()
            .any(|m| m["event"] == "actionResult" && m["payload"]["success"] == false)
        {
            plugin.close().unwrap();
            return;
        }
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
    panic!("failed application never completed its receipt");
}

#[test]
// Verify inspector hook preserves action handler fallback
fn inspector_hook_preserves_action_handler_fallback() {
    use std::sync::{
        atomic::{AtomicUsize, Ordering},
        Arc,
    };
    use stream_dock_manager_plugin_sdk::{
        authoring::{Action, Plugin},
        testkit::Recorder,
        Client, Event, HostInfo, Result,
    };
    struct InspectorAction(Arc<AtomicUsize>);
    impl Action for InspectorAction {
        // Dispatch the received event to the registered behavior
        fn handle(&mut self, _: &mut Plugin, _: &Client, event: &Event) -> Result<()> {
            if event.event == "sendToPlugin" {
                self.0.fetch_add(1, Ordering::SeqCst);
            }
            Ok(())
        }
    }
    let calls = Arc::new(AtomicUsize::new(0));
    let hooks = Arc::new(AtomicUsize::new(0));
    let mut plugin = Plugin::default();
    plugin
        .register("action", InspectorAction(calls.clone()))
        .unwrap();
    let recorder = Recorder::new(HostInfo::default());
    let event = |name: &str| {
        Event::parse(&serde_json::json!({"event":name,"context":"a","action":"action","payload":{"settings":{}}}).to_string()).unwrap()
    };
    recorder
        .replay(&mut plugin, &[event("willAppear"), event("sendToPlugin")])
        .unwrap();
    assert_eq!(calls.load(Ordering::SeqCst), 1);
    let count = hooks.clone();
    plugin.inspector = Some(Box::new(move |_, _, _| {
        count.fetch_add(1, Ordering::SeqCst);
        Ok(())
    }));
    recorder
        .replay(&mut plugin, &[event("sendToPlugin")])
        .unwrap();
    assert_eq!(hooks.load(Ordering::SeqCst), 1);
    assert_eq!(calls.load(Ordering::SeqCst), 1);
    plugin.inspector = None;
    recorder
        .replay(&mut plugin, &[event("sendToPlugin")])
        .unwrap();
    assert_eq!(calls.load(Ordering::SeqCst), 2);
}

#[test]
// Verify feedback numeric precision
fn feedback_numeric_precision() {
    for (a, b) in [
        (json!(9007199254740993u64), json!(9007199254740992.0)),
        (json!(1.0), json!(1.0000000000000002)),
        (json!(true), json!(1)),
    ] {
        let before = Display {
            feedback: BTreeMap::from([("value".into(), a)]),
            ..Default::default()
        };
        let after = Display {
            feedback: BTreeMap::from([("value".into(), b)]),
            ..Default::default()
        };
        assert_eq!(
            display_messages("a", Some(&before), &after, true, true).len(),
            1
        );
    }
}

#[test]
// Verify appearance lifecycle incarnations
fn appearance_lifecycle_incarnations() {
    use stream_dock_manager_plugin_sdk::Handler;
    struct Ready(Value);
    impl Action for Ready {
        // Produce the current display representation
        fn render(
            &mut self,
            _: &mut Plugin,
            _: &str,
            binding: &Binding,
        ) -> stream_dock_manager_plugin_sdk::Result<Option<Display>> {
            assert_eq!(binding.settings, self.0);
            Ok(Some(Display {
                title: "Ready".into(),
                ..Default::default()
            }))
        }
    }
    let spec = fixture()["appearance_lifecycle"].clone();
    let mut recorder = testkit::Recorder::new(HostInfo::default());
    let mut plugin = Plugin::default();
    plugin
        .register(
            spec["action"].as_str().unwrap(),
            Ready(spec["settings"].clone()),
        )
        .unwrap();
    for step in spec["steps"].as_array().unwrap() {
        recorder
            .replay(
                &mut plugin,
                &[Event::parse(&step["event"].to_string()).unwrap()],
            )
            .unwrap();
        let contexts: Vec<_> = plugin
            .bindings
            .keys()
            .cloned()
            .collect::<BTreeSet<_>>()
            .into_iter()
            .collect();
        assert_eq!(json!(contexts), step["bindings"]);
        let titles: Vec<_> = recorder
            .drain()
            .into_iter()
            .filter(|m| m["event"] == "setTitle")
            .map(|m| m["context"].clone())
            .collect();
        let expected = step["render_context"]
            .as_str()
            .map(|s| vec![json!(s)])
            .unwrap_or_default();
        assert_eq!(titles, expected);
    }
    plugin.close().unwrap();
}

#[test]
// Verify refresh policy and independent deadlines
fn refresh_policy_and_independent_deadlines() {
    let f = fixture();
    let policy: polling::RefreshPolicy =
        serde_json::from_value(f["refresh_policy"]["policy"].clone()).unwrap();
    for case in f["refresh_policy"]["cases"].as_array().unwrap() {
        let result = policy.resolve(&case["settings"]);
        assert_eq!(result.is_ok(), case["valid"].as_bool().unwrap(), "{case}");
        if let Ok(ms) = result {
            assert_eq!(ms, case["expected_ms"].as_u64().unwrap());
        }
    }
    let mut r = polling::Resources::default();
    for (id, interval) in [("fast", 1000), ("slow", 10000), ("off", 0)] {
        r.watch(id, "shared", interval).unwrap();
    }
    let g = r.begin("shared").unwrap();
    r.complete(
        "shared",
        g,
        polling::PollResult {
            value: json!(1),
            now: 100000,
            ..Default::default()
        },
    );
    let g = r.begin("shared").unwrap();
    r.complete(
        "shared",
        g,
        polling::PollResult {
            value: json!(2),
            now: 101000,
            ..Default::default()
        },
    );
    assert_eq!(r.snapshot("fast").unwrap().last_value, json!(2));
    assert_eq!(r.snapshot("slow").unwrap().last_value, json!(1));
    assert_eq!(r.snapshot("off").unwrap().last_value, json!(1));
    r.unwatch("fast");
    r.unwatch("slow");
    assert!(r.due(3700000).is_empty());
    r.states.get_mut("shared").unwrap().next = 0;
    let g = r.begin("shared").unwrap();
    r.complete(
        "shared",
        g,
        polling::PollResult {
            value: json!(3),
            now: 3700000,
            ..Default::default()
        },
    );
    assert_eq!(r.snapshot("off").unwrap().last_value, json!(3));
    r.states.get_mut("shared").unwrap().next = 0;
    r.watch("off", "shared", 0).unwrap();
    assert_eq!(r.due(3700000), vec!["shared"]);
    let g = r.begin("shared").unwrap();
    r.complete(
        "shared",
        g,
        polling::PollResult {
            failed: true,
            now: 3700000,
            retry_at: Some(7300000),
            ..Default::default()
        },
    );
    r.states.get_mut("shared").unwrap().next = 0;
    r.watch("new", "shared", 1000).unwrap();
    assert!(r.due(3700000).is_empty());
    assert_eq!(r.deadline("shared"), 7300000);
    let g = r.begin("shared").unwrap();
    r.complete(
        "shared",
        g,
        polling::PollResult {
            value: json!(4),
            now: 7300000,
            retry_at: Some(7360000),
            ..Default::default()
        },
    );
    r.states.get_mut("shared").unwrap().next = 0;
    assert!(r.due(7300000).is_empty());
}

#[test]
// Verify rejects obsolete state without overwrite
fn rejects_obsolete_state_without_overwrite() {
    let root = std::env::temp_dir().join(format!("sdk-current-state-{}", std::process::id()));
    std::fs::create_dir_all(&root).unwrap();
    let path = root.join("state.json");
    for raw in [
        r#"{"count":1}"#,
        r#"{"version":0,"data":{}}"#,
        r#"{"version":2,"data":{}}"#,
        r#"{"version":1}"#,
        "{",
    ] {
        std::fs::write(&path, raw).unwrap();
        let mut store = storage::Store::new(&path, 1);
        store.value = json!({"previous":true});
        assert!(store.load().is_err());
        assert_eq!(store.value, json!({"previous":true}));
        store.change(json!({"new":true}), 0);
        assert!(store.close().is_err());
        assert_eq!(std::fs::read_to_string(&path).unwrap(), raw);
    }
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
// Verify state version boundaries preserve files
fn state_version_boundaries_preserve_files() {
    for version in [0, 2, u32::MAX] {
        for exists in [false, true] {
            let root = temporary();
            let path = root.join("state.json");
            let raw = b"{\"version\":1,\"data\":{\"count\":7}}";
            if exists {
                std::fs::write(&path, raw).unwrap();
            }
            let check = || {
                if exists {
                    assert_eq!(std::fs::read(&path).unwrap(), raw);
                } else {
                    assert!(!path.exists());
                }
            };
            for dirty in [false, true] {
                for force in [false, true] {
                    let mut store = storage::Store::new(&path, 1);
                    store.version = version;
                    if dirty {
                        store.change(json!({"count":8}), 0);
                    }
                    assert!(store.flush(0, force).is_err());
                    assert_eq!(store.dirty, dirty);
                    assert!(store.close().is_err());
                    check();
                }
            }
            let mut store = storage::Store::new(&path, version);
            store.value = json!({"count":99});
            assert!(store.load().is_err());
            assert_eq!(store.value, json!({"count":99}));
            store.version = 1;
            store.change(json!({"count":7}), 0);
            assert!(store.close().is_err());
            check();
            store.load().unwrap();
            store.change(json!({"count":7}), 0);
            store.close().unwrap();
            assert_eq!(
                storage::Store::new(&path, 1).load().unwrap(),
                Some(json!({"count":7}))
            );
            std::fs::remove_dir_all(root).unwrap();
        }
    }
}
