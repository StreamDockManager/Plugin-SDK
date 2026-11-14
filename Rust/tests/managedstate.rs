use serde_json::{json, Map, Value};
use std::{
    collections::BTreeMap,
    sync::{Arc, Mutex},
};
use stream_dock_manager_plugin_sdk::{hoststorage::*, managedstate::*, Result};
#[derive(Default)]
struct Data {
    records: BTreeMap<String, Value>,
    revision: u64,
    receipts: BTreeMap<String, StorageReceipt>,
    commits: Vec<(StorageVersion, String, Vec<StorageMutation>)>,
    fail: bool,
    applied: bool,
    conflict: bool,
    cycle: bool,
}
impl Data {
    // Verify version
    fn version(&self) -> StorageVersion {
        StorageVersion {
            generation: "1".into(),
            revision: self.revision.to_string(),
        }
    }
}
#[derive(Clone)]
struct Memory(Arc<Mutex<Data>>);
// Build the structured failure used by the state contract
fn failure(code: &str) -> stream_dock_manager_plugin_sdk::Error {
    Box::new(RpcError {
        code: code.into(),
        message: "fixture".into(),
    })
}
// Derive the identity used to share or store state
fn key(id: &str) -> String {
    use sha2::{Digest, Sha256};
    format!(
        "record:{}",
        Sha256::digest(id)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    )
}
impl PrivateBackend for Memory {
    // List storage keys using the supplied continuation cursor
    fn list_keys(&self, _: Option<&str>) -> Result<StorageKeys> {
        let d = self.0.lock().unwrap();
        Ok(StorageKeys {
            version: d.version(),
            keys: d.records.keys().cloned().collect(),
            cursor: if d.cycle { Some("same".into()) } else { None },
        })
    }
    // Read the requested value through the current owner
    fn get(&self, key: &str) -> Result<StorageValue> {
        let d = self.0.lock().unwrap();
        Ok(StorageValue {
            version: d.version(),
            found: d.records.contains_key(key),
            value: d.records.get(key).cloned().unwrap_or(Value::Null),
        })
    }
    // Look up a previous transaction receipt without replaying writes
    fn receipt(&self, _: &str, id: &str) -> Result<StorageReceipt> {
        let d = self.0.lock().unwrap();
        Ok(d.receipts.get(id).cloned().unwrap_or(StorageReceipt {
            version: d.version(),
            operation_id: id.into(),
            found: false,
            committed: false,
        }))
    }
    // Submit the bounded storage transaction with its receipt identity
    fn commit(
        &self,
        version: &StorageVersion,
        id: &str,
        mutations: &[StorageMutation],
    ) -> Result<StorageReceipt> {
        let mut d = self.0.lock().unwrap();
        d.commits
            .push((version.clone(), id.into(), mutations.to_vec()));
        if d.conflict || *version != d.version() {
            return Err(failure("conflict"));
        }
        if d.fail && !d.applied {
            d.fail = false;
            return Err(failure("indeterminate"));
        }
        for mutation in mutations {
            match mutation {
                StorageMutation::Put { key, value } => {
                    d.records.insert(key.clone(), value.clone());
                }
                StorageMutation::Delete { key } => {
                    d.records.remove(key);
                }
            }
        }
        d.revision += 1;
        let receipt = StorageReceipt {
            version: d.version(),
            operation_id: id.into(),
            found: true,
            committed: true,
        };
        d.receipts.insert(id.into(), receipt.clone());
        if d.fail {
            d.fail = false;
            return Err(failure("indeterminate"));
        }
        Ok(receipt)
    }
}
// Construct the shared-read request specification
fn spec() -> Value {
    serde_json::from_str::<Value>(include_str!("../testdata/extensions-v1.json")).unwrap()
        ["managed_state"]
        .clone()
}
#[test]
// Verify shared managed state recovery
fn shared_managed_state_recovery() {
    let spec = spec();
    for recovery in spec["recoveries"].as_array().unwrap() {
        let backend = Memory(Arc::new(Mutex::new(Data {
            fail: true,
            applied: recovery == "applied",
            ..Data::default()
        })));
        for (id, value) in spec["initial"].as_object().unwrap() {
            backend
                .0
                .lock()
                .unwrap()
                .records
                .insert(key(id), json!({"identity":id,"value":value}));
        }
        let mut state = ManagedState::private(backend.clone());
        assert_eq!(state.load().unwrap(), spec["initial"]);
        assert!(state.save(&spec["next"]).is_err());
        assert!(state.dirty);
        assert!(state.check_pending().is_err());
        state.save(&spec["newer"]).unwrap();
        if recovery == "not-applied" {
            assert!(state.dirty);
            {
                let d = backend.0.lock().unwrap();
                assert_eq!(d.commits[0], d.commits[1]);
            }
            state.save(&spec["newer"]).unwrap();
        }
        assert!(!state.dirty);
        state.check_pending().unwrap();
        assert_eq!(state.assurance, "committed");
        {
            let d = backend.0.lock().unwrap();
            assert_eq!(d.records[&key("count")]["value"], spec["newer"]["count"]);
            assert!(!d.records.contains_key(&key("removed")));
        }
        let count = backend.0.lock().unwrap().commits.len();
        state.save(&spec["newer"]).unwrap();
        assert_eq!(backend.0.lock().unwrap().commits.len(), count);
        let large: Map<String, Value> = (0..33).map(|i| (i.to_string(), json!(i))).collect();
        assert!(state.save(&json!(large)).is_err());
        assert_eq!(backend.0.lock().unwrap().commits.len(), count);
    }
}
#[derive(Clone)]
struct Portable(Arc<Mutex<(Map<String, Value>, usize)>>);
impl PortableBackend for Portable {
    // Read data through the owned transport or state backend
    fn read(&self) -> Result<SettingsResult> {
        Ok(SettingsResult {
            settings: Some(self.0.lock().unwrap().0.clone()),
            backend: "base-vsd".into(),
            assurance: "observed".into(),
        })
    }
    // Apply the requested change while preserving owner state
    fn update(&self, changes: Map<String, Value>) -> Result<SettingsResult> {
        let mut d = self.0.lock().unwrap();
        d.0.extend(changes);
        d.1 += 1;
        Ok(SettingsResult {
            settings: None,
            backend: "base-vsd".into(),
            assurance: "sent".into(),
        })
    }
}
#[test]
// Verify portable session conflict and restore bounds
fn portable_session_conflict_and_restore_bounds() {
    let spec = spec();
    let backend = Portable(Arc::new(Mutex::new((
        json!({"unknown":true}).as_object().unwrap().clone(),
        0,
    ))));
    let mut state = ManagedState::portable(backend.clone(), "saved").unwrap();
    state.load().unwrap();
    state.save(&spec["next"]).unwrap();
    state.save(&spec["next"]).unwrap();
    assert_eq!(state.assurance, "sent");
    assert_eq!(backend.0.lock().unwrap().1, 1);
    assert_eq!(backend.0.lock().unwrap().0["unknown"], true);
    let mut state = ManagedState::session();
    state.load().unwrap();
    state.save(&spec["next"]).unwrap();
    assert_eq!(state.load().unwrap(), spec["next"]);
    let backend = Memory(Arc::new(Mutex::new(Data {
        conflict: true,
        ..Data::default()
    })));
    let mut state = ManagedState::private(backend.clone());
    state.load().unwrap();
    assert!(state.save(&spec["next"]).is_err());
    assert!(state.save(&spec["newer"]).is_err());
    assert_eq!(backend.0.lock().unwrap().commits.len(), 1);
    let backend = Memory(Arc::new(Mutex::new(Data {
        cycle: true,
        ..Data::default()
    })));
    assert!(ManagedState::private(backend).load().is_err());
}

#[test]
// Verify shared cache persistence batches and receipt recovery
fn shared_cache_persistence_batches_and_receipt_recovery() {
    use stream_dock_manager_plugin_sdk::cache::{Cache, Persistence};
    for applied in [false, true] {
        let backend = Memory(Arc::new(Mutex::new(Data {
            applied,
            ..Default::default()
        })));
        let mut c = Cache::<Value>::new(100, 100000).unwrap();
        let mut store = Persistence::new(backend.clone());
        store.load(&mut c, 0).unwrap();
        for i in 0..70 {
            c.put(&i.to_string(), json!(i), 16, 0, 0).unwrap();
        }
        backend.0.lock().unwrap().fail = true;
        assert!(store.flush(&mut c, 0).is_err());
        c.put("late", json!(99), 16, 0, 0).unwrap();
        store.flush(&mut c, 0).unwrap();
        {
            let d = backend.0.lock().unwrap();
            assert_eq!(d.records.len(), 71);
            assert!(d.commits.iter().all(|c| c.2.len() <= 32));
            if !applied {
                assert_eq!(d.commits[0].1, d.commits[1].1);
            }
        }
        let mut restored = Cache::<Value>::new(100, 100000).unwrap();
        Persistence::new(backend.clone())
            .load(&mut restored, 0)
            .unwrap();
        assert_eq!(restored.get("late", 0), Some(json!(99)));
        c.put("late", json!(100), 16, 0, 0).unwrap();
        backend.0.lock().unwrap().conflict = true;
        assert!(store.flush(&mut c, 0).is_err());
        let attempts = backend.0.lock().unwrap().commits.len();
        assert!(store.flush(&mut c, 0).is_err());
        assert_eq!(backend.0.lock().unwrap().commits.len(), attempts);
    }
}

#[test]
// Verify cache batches by wire bytes
fn cache_batches_by_wire_bytes() {
    use stream_dock_manager_plugin_sdk::cache::{Cache, Persistence};
    let backend = Memory(Arc::new(Mutex::new(Data::default())));
    let mut c = Cache::<Value>::new(10, 1 << 20).unwrap();
    let mut store = Persistence::new(backend.clone());
    store.load(&mut c, 0).unwrap();
    for i in 0..3 {
        c.put(&i.to_string(), json!("x".repeat(210000)), 220000, 0, 0)
            .unwrap();
    }
    store.flush(&mut c, 0).unwrap();
    let data = backend.0.lock().unwrap();
    assert_eq!(data.commits.len(), 3);
    assert!(data
        .commits
        .iter()
        .all(|c| serde_json::to_vec(&c.2).unwrap().len() < 512 << 10));
}
