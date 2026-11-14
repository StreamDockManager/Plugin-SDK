//! Managed JSON records. Select the backend explicitly; no hidden local fallback
use crate::{error, hoststorage::*, Result};
use serde_json::{json, Map, Value};
use sha2::{Digest, Sha256};
use std::collections::BTreeSet;
pub trait PortableBackend: Send {
    // Read data through the owned transport or state backend for managed state persistence
    fn read(&self) -> Result<SettingsResult>;
    // Apply the requested change while preserving owner state for managed state persistence
    fn update(&self, changes: Map<String, Value>) -> Result<SettingsResult>;
}
impl PortableBackend for PortableSettings {
    // Read data through the owned transport or state backend for managed state persistence
    fn read(&self) -> Result<SettingsResult> {
        PortableSettings::read(self)
    }
    // Apply the requested change while preserving owner state for managed state persistence
    fn update(&self, changes: Map<String, Value>) -> Result<SettingsResult> {
        PortableSettings::update(self, changes)
    }
}
pub trait PrivateBackend: Send {
    // List storage keys using the supplied continuation cursor
    fn list_keys(&self, cursor: Option<&str>) -> Result<StorageKeys>;
    // Read the requested value through the current owner for managed state persistence
    fn get(&self, key: &str) -> Result<StorageValue>;
    // Look up a previous transaction receipt without replaying writes
    fn receipt(&self, generation: &str, id: &str) -> Result<StorageReceipt>;
    // Submit the bounded storage transaction with its receipt identity
    fn commit(
        &self,
        version: &StorageVersion,
        id: &str,
        mutations: &[StorageMutation],
    ) -> Result<StorageReceipt>;
}
impl PrivateBackend for HostStore {
    // List storage keys using the supplied continuation cursor
    fn list_keys(&self, cursor: Option<&str>) -> Result<StorageKeys> {
        HostStore::list_keys(self, cursor)
    }
    // Read the requested value through the current owner for managed state persistence
    fn get(&self, key: &str) -> Result<StorageValue> {
        HostStore::get(self, key)
    }
    // Look up a previous transaction receipt without replaying writes
    fn receipt(&self, generation: &str, id: &str) -> Result<StorageReceipt> {
        HostStore::receipt(self, generation, id)
    }
    // Submit the bounded storage transaction with its receipt identity
    fn commit(
        &self,
        version: &StorageVersion,
        id: &str,
        mutations: &[StorageMutation],
    ) -> Result<StorageReceipt> {
        HostStore::commit(self, version, id, mutations)
    }
}
enum Backend {
    Session,
    Portable(Box<dyn PortableBackend>, String),
    Private(Box<dyn PrivateBackend>),
}
struct Pending {
    id: String,
    version: StorageVersion,
    mutations: Vec<StorageMutation>,
    snapshot: Map<String, Value>,
}
pub struct ManagedState {
    backend: Backend,
    ready: bool,
    written: Map<String, Value>,
    version: Option<StorageVersion>,
    pending: Option<Pending>,
    blocked: Option<RpcError>,
    pub dirty: bool,
    pub assurance: String,
}
// Build the structured failure used by the state contract
fn failure(code: &str, message: &str) -> crate::Error {
    Box::new(RpcError {
        code: code.into(),
        message: message.into(),
    })
}
// Derive the identity used to share or store state for managed state persistence
fn key(identity: &str) -> String {
    format!(
        "record:{}",
        Sha256::digest(identity.as_bytes())
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    )
}
// Copy the current observable state for managed state persistence
fn snapshot(value: &Value) -> Result<Map<String, Value>> {
    let result = value
        .as_object()
        .ok_or_else(|| error("managed state must be an object"))?;
    if result.keys().any(String::is_empty) {
        return Err(error("managed state identities must not be empty"));
    }
    if result.len() > 4096 || serde_json::to_vec(value)?.len() > 8 << 20 {
        return Err(failure("quota", "managed state exceeds restore budget"));
    }
    Ok(result.clone())
}
impl ManagedState {
    // Initialize the instance and its owned state for managed state persistence
    fn new(backend: Backend) -> Self {
        Self {
            backend,
            ready: false,
            written: Map::new(),
            version: None,
            pending: None,
            blocked: None,
            dirty: false,
            assurance: String::new(),
        }
    }
    // Create a session-scoped managed-state backend
    pub fn session() -> Self {
        Self::new(Backend::Session)
    }
    // Create a portable-settings managed-state backend
    pub fn portable(
        backend: impl PortableBackend + 'static,
        key: impl Into<String>,
    ) -> Result<Self> {
        let key = key.into();
        if key.is_empty() {
            return Err(error("portable state key is required"));
        }
        Ok(Self::new(Backend::Portable(Box::new(backend), key)))
    }
    // Create a private host-storage managed-state backend
    pub fn private(backend: impl PrivateBackend + 'static) -> Self {
        Self::new(Backend::Private(Box::new(backend)))
    }
    // Mark the current state as awaiting persistence
    pub fn mark_dirty(&mut self) {
        self.dirty = true
    }
    // Resolve the pending transaction receipt before retrying
    pub fn check_pending(&self) -> Result<()> {
        if self.dirty && !matches!(self.backend, Backend::Session) {
            return Err(error("managed state has an unconfirmed write"));
        }
        Ok(())
    }
    // Read and validate persisted state before exposing it
    pub fn load(&mut self) -> Result<Value> {
        if self.ready {
            return Ok(Value::Object(self.written.clone()));
        }
        let mut values = Map::new();
        match &self.backend {
            Backend::Session => self.assurance = "session".into(),
            Backend::Portable(backend, key) => {
                let result = backend.read()?;
                self.assurance = result.assurance;
                if let Some(value) = result
                    .settings
                    .as_ref()
                    .and_then(|settings| settings.get(key))
                {
                    values = snapshot(value)?
                }
            }
            Backend::Private(backend) => {
                let mut cursor = String::new();
                let mut seen = BTreeSet::new();
                let mut total = 0;
                loop {
                    if !seen.insert(cursor.clone()) || seen.len() > 4096 {
                        return Err(error(
                            "repeated storage cursor or restore page budget exceeded",
                        ));
                    }
                    let page = backend.list_keys(Some(&cursor))?;
                    if cursor.is_empty() {
                        self.version = Some(page.version.clone())
                    } else if Some(&page.version) != self.version.as_ref() {
                        return Err(failure("conflict", "private state changed during load"));
                    }
                    for name in page.keys {
                        let item = backend.get(&name)?;
                        if !item.found || Some(&item.version) != self.version.as_ref() {
                            return Err(failure("conflict", "private state changed during load"));
                        }
                        total += serde_json::to_vec(&item.value)?.len();
                        if total > 8 << 20 || values.len() >= 4096 {
                            return Err(failure("quota", "managed state exceeds restore budget"));
                        }
                        let identity = item.value["identity"]
                            .as_str()
                            .ok_or_else(|| error("invalid managed state record"))?;
                        if identity.is_empty()
                            || key(identity) != name
                            || values.contains_key(identity)
                        {
                            return Err(error("invalid managed state record"));
                        }
                        values.insert(
                            identity.into(),
                            item.value
                                .get("value")
                                .ok_or_else(|| error("invalid managed state record"))?
                                .clone(),
                        );
                    }
                    cursor = page.cursor.unwrap_or_default();
                    if cursor.is_empty() {
                        break;
                    }
                }
                self.assurance = "observed".into();
            }
        }
        self.written = snapshot(&Value::Object(values))?;
        self.ready = true;
        Ok(Value::Object(self.written.clone()))
    }
    // Persist the current state through its selected backend
    pub fn save(&mut self, value: &Value) -> Result<()> {
        if !self.ready {
            return Err(error("managed state is not initialized"));
        }
        if let Some(blocked) = &self.blocked {
            return Err(Box::new(blocked.clone()));
        }
        let current = snapshot(value)?;
        self.dirty = true;
        let backend = match &self.backend {
            Backend::Session => {
                self.written = current;
                self.dirty = false;
                return Ok(());
            }
            Backend::Portable(backend, key) => {
                if current != self.written {
                    if serde_json::to_vec(&current)?.len() > 256 << 10 {
                        return Err(failure("quota", "portable runtime state exceeds 256 KiB"));
                    }
                    let mut changes = Map::new();
                    changes.insert(key.clone(), Value::Object(current.clone()));
                    self.assurance = backend.update(changes)?.assurance;
                }
                self.written = current;
                self.dirty = false;
                return Ok(());
            }
            Backend::Private(backend) => backend,
        };
        if let Some(pending) = &self.pending {
            let receipt = backend.receipt(&pending.version.generation, &pending.id)?;
            if receipt.found && receipt.committed {
                self.version = Some(receipt.version);
                self.assurance = "committed".into();
                self.written = pending.snapshot.clone();
                self.pending = None
            }
        }
        if self.pending.is_none() {
            let keys: BTreeSet<_> = current
                .keys()
                .chain(self.written.keys())
                .filter(|key| current.get(*key) != self.written.get(*key))
                .cloned()
                .collect();
            if keys.len() > 32 {
                return Err(failure(
                    "quota",
                    "managed state change exceeds one atomic 32-record commit",
                ));
            }
            if keys.is_empty() {
                self.dirty = false;
                return Ok(());
            }
            let mut mutations = Vec::new();
            for identity in keys {
                let mutation = if let Some(value) = current.get(&identity) {
                    let record = json!({"identity":identity,"value":value});
                    if serde_json::to_vec(&record)?.len() > 256 << 10 {
                        return Err(failure("quota", "managed state record exceeds 256 KiB"));
                    }
                    StorageMutation::Put {
                        key: key(&identity),
                        value: record,
                    }
                } else {
                    StorageMutation::Delete {
                        key: key(&identity),
                    }
                };
                mutations.push(mutation);
            }
            self.pending = Some(Pending {
                id: operation_id(),
                version: self.version.clone().unwrap(),
                mutations,
                snapshot: current.clone(),
            });
        }
        let pending = self.pending.as_ref().unwrap();
        let receipt = match backend.commit(&pending.version, &pending.id, &pending.mutations) {
            Ok(receipt) => receipt,
            Err(error) => {
                if let Some(failure) = error.downcast_ref::<RpcError>() {
                    if failure.code == "conflict" {
                        self.blocked = Some(failure.clone())
                    }
                }
                return Err(error);
            }
        };
        self.version = Some(receipt.version);
        self.assurance = "committed".into();
        self.written = pending.snapshot.clone();
        self.pending = None;
        self.dirty = current != self.written;
        Ok(())
    }
}
