//! Opt-in allocation traffic comparison for the frozen pre-R18 flush algorithm
use serde_json::json;
use std::{
    alloc::{GlobalAlloc, Layout, System},
    cell::Cell,
};
use stream_dock_manager_plugin_sdk::storage;
struct Counted;
thread_local! {
    static ACTIVE: Cell<bool> = const { Cell::new(false) };
    static REQUESTED: Cell<usize> = const { Cell::new(0) };
}
// Verify record
fn record(size: usize) {
    let _ = ACTIVE.try_with(|active| {
        if active.get() {
            let _ = REQUESTED.try_with(|value| value.set(value.get() + size));
        }
    });
}
unsafe impl GlobalAlloc for Counted {
    // Verify alloc
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        record(layout.size());
        unsafe { System.alloc(layout) }
    }
    // Verify alloc zeroed
    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        record(layout.size());
        unsafe { System.alloc_zeroed(layout) }
    }
    // Verify dealloc
    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { System.dealloc(ptr, layout) }
    }
    // Verify realloc
    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, size: usize) -> *mut u8 {
        record(size);
        unsafe { System.realloc(ptr, layout, size) }
    }
}
#[global_allocator]
static ALLOCATOR: Counted = Counted;
// Verify requested
fn requested(action: impl FnOnce()) -> usize {
    REQUESTED.with(|value| value.set(0));
    ACTIVE.with(|value| value.set(true));
    action();
    ACTIVE.with(|value| value.set(false));
    REQUESTED.with(Cell::get)
}
#[test]
#[ignore = "allocation evidence; run independently in release mode"]
// Verify changed flush reuses encoded bytes without cloning envelope value
fn changed_flush_reuses_encoded_bytes_without_cloning_envelope_value() {
    let root = std::env::temp_dir().join(format!("sdm-state-alloc-{}", std::process::id()));
    std::fs::create_dir_all(&root).unwrap();
    for repetition in 0..5 {
        let value = json!({"opaque": "x".repeat(4 << 20), "unknown": [false, null, 1.25, "é😀"]});
        let old_path = root.join("old.json");
        let path = root.join("current.json");
        let mut store = storage::Store::new(&path, 7);
        store.change(value, 0);
        let old = requested(|| {
            // Original HEAD 494c4e27 storage::Store::flush: owned envelope,
            // first encode for comparison, second encode inside public write
            let envelope = json!({"version":store.version,"data":store.value});
            let data = serde_json::to_vec(&envelope).unwrap();
            let written = storage::write(&old_path, &envelope).unwrap();
            std::hint::black_box((data, written));
        });
        let current = requested(|| {
            store.close().unwrap();
        });
        assert_eq!(
            std::fs::read(&path).unwrap(),
            std::fs::read(&old_path).unwrap()
        );
        assert!(current < old / 2, "{old} -> {current}");
        println!("STATE_ALLOCATION repetition={repetition} baseline_requested_bytes={old} candidate_requested_bytes={current} encoded_bytes={}", std::fs::metadata(&path).unwrap().len());
    }
    std::fs::remove_dir_all(root).unwrap();
}
