use serde_json::{json, Value};
use stream_dock_manager_plugin_sdk::{authoring::*, testkit, Event, Handler, HostInfo};
#[test]
// Verify shared semantic compact layouts
fn shared_semantic_compact_layouts() {
    let fixture: Value =
        serde_json::from_str(include_str!("../testdata/extensions-v1.json")).unwrap();
    for case in fixture["layouts"].as_array().unwrap() {
        let base:Display=serde_json::from_value(json!({"title":"Full title","image":"icon.svg","layout":"custom.json","feedback":{"custom":{"value":"kept"}}})).unwrap();
        let content: Presentation = serde_json::from_value(case["content"].clone()).unwrap();
        let width = case["width"].as_u64().unwrap() as u32;
        let height = case["height"].as_u64().unwrap() as u32;
        let result = compact_display(&present(&base, &content, width, height), width, height);
        assert_eq!(base.feedback.len(), 1);
        assert_eq!(result.title, base.title);
        assert_eq!(result.layout, base.layout);
        for (key, rect) in case["rects"].as_object().unwrap() {
            assert_eq!(result.feedback[key]["rect"], *rect, "{}", case["name"])
        }
        if !case["progress"].is_null() {
            assert_eq!(
                result.feedback["indicator"]["value"].as_f64(),
                case["progress"].as_f64()
            )
        }
    }
}
#[test]
// Verify observation receipt isolation
fn observation_receipt_isolation() {
    use std::sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    };
    for failure in [false, true] {
        struct Start {
            failure: bool,
            release: Arc<AtomicBool>,
            applied: Arc<AtomicBool>,
        }
        impl Action for Start {
            // Dispatch the received event to the registered behavior
            fn handle(
                &mut self,
                p: &mut Plugin,
                c: &stream_dock_manager_plugin_sdk::Client,
                e: &Event,
            ) -> stream_dock_manager_plugin_sdk::Result<()> {
                let release = self.release.clone();
                let applied = self.applied.clone();
                let failure = self.failure;
                assert!(p
                    .observe(
                        c,
                        "read",
                        &e.context,
                        move |_| {
                            while !release.load(Ordering::Acquire) {
                                std::thread::sleep(std::time::Duration::from_millis(1))
                            }
                            if failure {
                                Err(std::io::Error::other("read failed").into())
                            } else {
                                Ok(json!(42))
                            }
                        },
                        move |_, _, result| {
                            assert_eq!(result.is_err(), failure);
                            applied.store(true, Ordering::Release);
                            Ok(())
                        }
                    )
                    .is_some());
                assert!(p
                    .observe(
                        c,
                        "read",
                        &e.context,
                        |_| Ok(json!(0)),
                        |_, _, _| panic!("duplicate")
                    )
                    .is_none());
                Ok(())
            }
        }
        let fixture: Value =
            serde_json::from_str(include_str!("../testdata/protocol-v1.json")).unwrap();
        let mut r =
            testkit::Recorder::new(HostInfo::parse(&fixture["manager"].to_string()).unwrap());
        let mut p = Plugin::default();
        let release = Arc::new(AtomicBool::new(false));
        let applied = Arc::new(AtomicBool::new(false));
        p.register(
            "command",
            Start {
                failure,
                release: release.clone(),
                applied: applied.clone(),
            },
        )
        .unwrap();
        r.replay(&mut p,&[Event::parse(r#"{"event":"keyDown","action":"command","context":"key","streamDockManager":{"request_id":"command"}}"#).unwrap()]).unwrap();
        let events = r.drain();
        let receipts: Vec<_> = events
            .iter()
            .filter(|e| e["event"] == "actionResult")
            .collect();
        assert_eq!(receipts.len(), 1);
        assert_eq!(receipts[0]["payload"]["success"], true);
        release.store(true, Ordering::Release);
        for _ in 0..100 {
            std::thread::sleep(std::time::Duration::from_millis(1));
            r.poll(&mut p).unwrap();
            if applied.load(Ordering::Acquire) {
                break;
            }
        }
        assert!(applied.load(Ordering::Acquire));
        assert!(!r.drain().iter().any(|e| e["event"] == "actionResult"));
        p.close().unwrap();
    }
}

#[test]
// Verify discovery packaged controller
fn discovery_packaged_controller() {
    use std::{
        io::Write,
        process::{Command, Stdio},
    };
    let mut child =
        Command::new(std::env::var("STREAM_DOCK_MANAGER_NODE").unwrap_or("node".into()))
            .args(["testdata/discovery.mjs", "-"])
            .stdin(Stdio::piped())
            .spawn()
            .unwrap();
    child
        .stdin
        .take()
        .unwrap()
        .write_all(stream_dock_manager_plugin_sdk::inspectors::WORKFLOWS.as_bytes())
        .unwrap();
    assert!(child.wait().unwrap().success());
}

#[cfg(feature = "thumbnail")]
#[test]
// Verify thumbnail shared formats and limits
fn thumbnail_shared_formats_and_limits() {
    use base64::Engine;
    use std::sync::atomic::AtomicBool;
    use stream_dock_manager_plugin_sdk::thumbnail::*;
    let fixture: Value =
        serde_json::from_str(include_str!("../testdata/extensions-v1.json")).unwrap();
    let cancel = AtomicBool::new(false);
    for c in fixture["thumbnails"].as_array().unwrap() {
        let data = base64::engine::general_purpose::STANDARD
            .decode(c["data"].as_str().unwrap())
            .unwrap();
        let image = decode(
            &data,
            c["width"].as_u64().unwrap() as u32,
            c["height"].as_u64().unwrap() as u32,
            &cancel,
        )
        .unwrap();
        let expected = c["rgba"].as_array().unwrap();
        assert_eq!(image.data.len(), expected.len());
        for (i, actual) in image.data.iter().enumerate() {
            if i % 4 != 3 && expected[i - i % 4 + 3] == 0 {
                continue;
            }
            assert!(
                (i64::from(*actual) - expected[i].as_i64().unwrap()).abs()
                    <= c["tolerance"].as_i64().unwrap(),
                "{}:{}:{} != {}",
                c["name"],
                i,
                actual,
                expected[i]
            )
        }
        assert!(decode(&data, 321, 1, &cancel).is_err());
        assert!(decode(&data, 2, 2, &AtomicBool::new(true)).is_err());
    }
    for data in [vec![], b"invalid".to_vec(), vec![0; MAX_INPUT_BYTES + 1]] {
        assert!(decode(&data, 1, 1, &cancel).is_err())
    }
}
