use serde_json::{json, Map, Value};
#[cfg(feature = "thumbnail")]
use std::sync::atomic::AtomicBool;
use std::{collections::BTreeSet, sync::atomic::Ordering};
use stream_dock_manager_plugin_sdk::{
    cache::{Budget, Cache},
    discovery::{envelope, Request, Runner, MAX_PENDING, MAX_RESPONSE_BYTES},
    operations::{Ledger, State},
};
// Verify fixture
fn fixture() -> Value {
    serde_json::from_str(include_str!("../testdata/helpers-v1.json")).unwrap()
}
#[test]
// Verify shared cache
fn shared_cache() {
    let f = fixture();
    let s = &f["cache"];
    let mut c = Cache::new(
        s["capacity"].as_u64().unwrap() as usize,
        s["bytes"].as_u64().unwrap() as usize,
    )
    .unwrap();
    let a = s["keys"][0].as_str().unwrap();
    let b = s["keys"][1].as_str().unwrap();
    let d = s["keys"][2].as_str().unwrap();
    let cost = s["cost"].as_u64().unwrap() as usize;
    let expiry = s["expiry"].as_u64().unwrap();
    c.put(a, "A", cost, 0, 0).unwrap();
    c.put(b, "B", cost, 0, 0).unwrap();
    c.get(a, 1);
    assert_eq!(c.put(d, "C", cost, expiry, 2).unwrap(), vec![b]);
    assert!(c.put(a, "bad", 1000, 0, 3).is_err());
    assert_eq!(c.peek(a, 3), Some("A"));
    assert_eq!(c.get(d, expiry), None);
    assert_eq!(c.bytes(), 75);
    assert_eq!(c.trim(200, 31).unwrap(), vec![a]);
    assert_eq!(c.bytes(), 0);
    let budget = Budget::new(100).unwrap();
    assert!(budget.reserve(80));
    assert!(!budget.reserve(21));
    assert!(budget.release(81).is_err());
    assert_eq!(budget.used(), 80);
    budget.release(80).unwrap();
}
#[test]
// Verify shared operations
fn shared_operations() {
    let f = fixture();
    let s = &f["operations"];
    let mut l = Ledger::new(
        s["capacity"].as_u64().unwrap() as usize,
        s["bytes"].as_u64().unwrap() as usize,
    )
    .unwrap();
    let group = s["group"].as_str().unwrap();
    let cost = s["cost"].as_u64().unwrap() as usize;
    let a = l.reserve(group, "first", cost).unwrap();
    let b = l.reserve(group, "second", cost).unwrap();
    let c = l.reserve("other", "third", cost).unwrap();
    assert_eq!(json!([a.id, b.id, c.id]), s["ids"]);
    l.update(&b.id, State::Uncertain, "unknown", cost, false)
        .unwrap();
    assert!(l.reserve("fourth", "no", cost).is_err());
    assert!(l.retire(&BTreeSet::new()).is_empty());
    l.update(&a.id, State::Confirmed, "done", cost, true)
        .unwrap();
    assert!(l.retire(&BTreeSet::new()).is_empty());
    l.update(&a.id, State::Confirmed, "done", cost, false)
        .unwrap();
    assert!(l.retire(&BTreeSet::from([group.into()])).is_empty());
    assert_eq!(l.retire(&BTreeSet::new()), vec![a.id]);
    let before = l.bytes();
    assert!(l
        .update(&b.id, State::Confirmed, "too big", 2048, false)
        .is_err());
    assert_eq!(l.bytes(), before);
    assert_eq!(l.get(&b.id).unwrap().state, State::Uncertain);
    l.reserve("fourth", "ok", cost).unwrap();
}
#[test]
// Verify shared discovery
fn shared_discovery() {
    let f = fixture();
    let s = &f["discovery"];
    let now = s["now"].as_u64().unwrap();
    let timeout = s["timeout"].as_u64().unwrap();
    let mut r = Runner::default();
    let mut req = Request {
        context: s["context"].as_str().unwrap().into(),
        channel: s["channel"].as_str().unwrap().into(),
        operation: s["operation"].as_str().unwrap().into(),
        id: s["first"].as_str().unwrap().into(),
        ..Default::default()
    };
    for id in s["invalid_ids"].as_array().unwrap() {
        let mut invalid = req.clone();
        invalid.id = id.as_str().unwrap().into();
        assert!(r.start(invalid.clone(), now, timeout).is_err());
        assert!(envelope(&invalid, Map::new(), "").is_err());
    }
    let mut isolated = req.clone();
    isolated.context = "other-context".into();
    let other = r.start(isolated.clone(), now, timeout).unwrap();
    let duplicate = r.start(req.clone(), now, timeout).unwrap();
    let a = r.start(req.clone(), now, timeout).unwrap();
    assert!(duplicate.cancel.load(Ordering::Acquire));
    assert!(!other.cancel.load(Ordering::Acquire));
    assert!(r
        .complete(&duplicate, Map::new(), "", now)
        .unwrap()
        .is_none());
    assert!(r.cancel(&isolated.context, &isolated.channel, &isolated.id));
    assert!(other.cancel.load(Ordering::Acquire));

    let old = req.id.clone();
    req.id = s["second"].as_str().unwrap().into();
    let b = r.start(req.clone(), now + 1, timeout).unwrap();
    assert!(a.cancel.load(Ordering::Acquire));
    assert!(!r.cancel(&req.context, &req.channel, &old));
    assert!(r.complete(&a, Map::new(), "", now + 2).unwrap().is_none());
    let reply = r
        .complete(
            &b,
            json!({"request_id":"spoof","op":"spoof","devices":["one"]})
                .as_object()
                .unwrap()
                .clone(),
            "",
            now + 2,
        )
        .unwrap()
        .unwrap();
    assert_eq!(reply.payload["request_id"], req.id);
    assert_eq!(reply.payload["op"], req.operation);
    assert_eq!(reply.payload["available"], true);
    let t = r.start(req.clone(), now, timeout).unwrap();
    assert_eq!(r.next_deadline(), now + timeout);
    assert_eq!(
        r.expire(now + timeout).unwrap()[0].payload["error"],
        "timeout"
    );
    assert!(t.cancel.load(Ordering::Acquire));
    assert_eq!(
        envelope(
            &req,
            Map::from_iter([("data".into(), json!("x".repeat(MAX_RESPONSE_BYTES)))]),
            ""
        )
        .unwrap()
        .payload["error"],
        "response_too_large"
    );
    for i in 0..MAX_PENDING {
        let mut req = req.clone();
        req.context = i.to_string();
        r.start(req, 0, 1).unwrap();
    }
    req.context = "overflow".into();
    assert!(r.start(req.clone(), 0, 1).is_err());
    r.clear("0");
    r.start(req.clone(), 0, 1).unwrap();
    r.close();
    assert!(r.start(req, 0, 1).is_err());
}
#[cfg(feature = "thumbnail")]
#[test]
// Verify shared thumbnail helpers
fn shared_thumbnail_helpers() {
    use base64::Engine;
    use stream_dock_manager_plugin_sdk::thumbnail::*;
    let f = fixture();
    let s = &f["thumbnails"];
    assert_eq!(
        MAX_INPUT_BYTES,
        s["max_input_bytes"].as_u64().unwrap() as usize
    );
    assert_eq!(MAX_SOURCE_PIXELS, s["max_source_pixels"].as_u64().unwrap());
    for c in s["fit"].as_array().unwrap() {
        let num = |field: &str, index: usize| c[field][index].as_u64().unwrap() as u32;
        let limits = Limits {
            max_source_pixels: c["pixels"].as_u64().unwrap_or(MAX_SOURCE_PIXELS),
            ..Default::default()
        };
        assert_eq!(
            fit_with_limits(
                num("source", 0),
                num("source", 1),
                num("box", 0),
                num("box", 1),
                limits
            )
            .unwrap(),
            (num("output", 0), num("output", 1))
        );
    }
    assert!(fit(8000, 8000, 320, 320).is_err());
    let cancel = AtomicBool::new(false);
    let mut source = Thumbnail {
        width: 3,
        height: 2,
        data: vec![0; 24],
    };
    source.data[..4].copy_from_slice(&[12, 34, 56, 128]);
    let encoded = png(&source, &cancel).unwrap();
    assert!(decode_with_limits(
        &encoded,
        3,
        2,
        &cancel,
        Limits {
            max_input_bytes: encoded.len() - 1,
            ..Default::default()
        }
    )
    .is_err());
    assert!(decode_with_limits(
        &encoded,
        3,
        2,
        &cancel,
        Limits {
            max_source_pixels: 5,
            ..Default::default()
        }
    )
    .is_err());
    let rendered = render_with_limits(
        &encoded,
        320,
        320,
        &cancel,
        Limits {
            max_input_bytes: encoded.len(),
            max_source_pixels: 6,
        },
    )
    .unwrap();
    assert_eq!((rendered.width, rendered.height), (3, 2));
    let data = base64::engine::general_purpose::STANDARD
        .decode(rendered.data_uri.split_once(',').unwrap().1)
        .unwrap();
    assert_eq!(decode(&data, 3, 2, &cancel).unwrap().data, source.data);
    assert!(decode_with_limits(
        &encoded,
        1,
        1,
        &cancel,
        Limits {
            max_source_pixels: 0,
            ..Default::default()
        }
    )
    .is_err());
    cancel.store(true, Ordering::Release);
    assert!(png(&source, &cancel).is_err());
}
