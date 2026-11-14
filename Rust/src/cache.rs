//! Owner-serialized LRU caches; logical byte accounting, not process RSS
use crate::{
    error,
    managedstate::{ManagedState, PrivateBackend},
    Result,
};
use serde::{de::DeserializeOwned, Serialize};
use serde_json::{json, Map, Value};
use std::{collections::VecDeque, sync::Mutex};
pub const MAX_ENTRIES: usize = 65536;
pub const MAX_BYTES: usize = 64 << 20;
pub const MAX_TIME: u64 = 9007199254740991;
#[derive(Clone, Debug)]
pub struct Entry<T> {
    pub key: String,
    pub value: T,
    pub cost: usize,
    pub expires: u64,
}
impl<T> Entry<T> {
    // Calculate the logical memory charge including bookkeeping
    fn charge(&self) -> usize {
        self.cost + self.key.len() + 64
    }
}
pub struct Cache<T> {
    values: VecDeque<Entry<T>>,
    capacity: usize,
    limit: usize,
    bytes: usize,
}
impl<T: Clone> Cache<T> {
    // Initialize the instance and its owned state for bounded cache storage
    pub fn new(capacity: usize, limit: usize) -> Result<Self> {
        if capacity == 0 || capacity > MAX_ENTRIES || limit == 0 || limit > MAX_BYTES {
            return Err(error("invalid cache limits"));
        }
        Ok(Self {
            values: VecDeque::new(),
            capacity,
            limit,
            bytes: 0,
        })
    }
    // Return the number of retained entries
    pub fn len(&self) -> usize {
        self.values.len()
    }
    // Report whether no entries are retained
    pub fn is_empty(&self) -> bool {
        self.values.is_empty()
    }
    // Return the logical byte charge of retained entries
    pub fn bytes(&self) -> usize {
        self.bytes
    }
    // Remove the named entry and release its logical byte charge
    pub fn delete(&mut self, key: &str) -> bool {
        if let Some(at) = self.values.iter().position(|e| e.key == key) {
            self.bytes -= self.values.remove(at).unwrap().charge();
            true
        } else {
            false
        }
    }
    // Discard retained state and reset its bookkeeping
    pub fn clear(&mut self) {
        self.values.clear();
        self.bytes = 0;
    }
    // Read a live cache value without promoting its recency
    pub fn peek(&mut self, key: &str, now: u64) -> Option<T> {
        if now > MAX_TIME {
            return None;
        }
        let at = self.values.iter().position(|e| e.key == key)?;
        let e = &self.values[at];
        if e.expires != 0 && e.expires <= now {
            self.delete(key);
            None
        } else {
            Some(e.value.clone())
        }
    }
    // Read the requested value through the current owner for bounded cache storage
    pub fn get(&mut self, key: &str, now: u64) -> Option<T> {
        let v = self.peek(key, now)?;
        let at = self.values.iter().position(|e| e.key == key).unwrap();
        let e = self.values.remove(at).unwrap();
        self.values.push_back(e);
        Some(v)
    }
    // Return a snapshot of the retained cache entries
    pub fn entries(&mut self, now: u64) -> Result<Vec<Entry<T>>> {
        if now > MAX_TIME {
            return Err(error("invalid cache time"));
        }
        let expired: Vec<_> = self
            .values
            .iter()
            .filter(|e| e.expires != 0 && e.expires <= now)
            .map(|e| e.key.clone())
            .collect();
        for k in expired {
            self.delete(&k);
        }
        Ok(self.values.iter().cloned().collect())
    }
    // Evict old entries until configured limits are satisfied
    pub fn trim(&mut self, reserved: usize, now: u64) -> Result<Vec<String>> {
        if reserved > self.limit || now > MAX_TIME {
            return Err(error("invalid cache reservation"));
        }
        let mut out: Vec<_> = self
            .values
            .iter()
            .filter(|e| e.expires != 0 && e.expires <= now)
            .map(|e| e.key.clone())
            .collect();
        out.sort();
        for k in &out {
            self.delete(k);
        }
        while self.bytes + reserved > self.limit || self.len() > self.capacity {
            let e = self.values.pop_front().unwrap();
            self.bytes -= e.charge();
            out.push(e.key);
        }
        Ok(out)
    }
    // Insert a value while enforcing cache expiration and capacity
    pub fn put(
        &mut self,
        key: &str,
        value: T,
        cost: usize,
        expires: u64,
        now: u64,
    ) -> Result<Vec<String>> {
        if key.is_empty()
            || key.len() > 4096
            || cost > self.limit
            || cost + key.len() + 64 > self.limit
            || expires > MAX_TIME
            || now > MAX_TIME
        {
            return Err(error("invalid or oversized cache entry"));
        }
        self.delete(key);
        let mut out = self.trim(0, now)?;
        if expires != 0 && expires <= now {
            return Ok(out);
        }
        let e = Entry {
            key: key.into(),
            value,
            cost,
            expires,
        };
        self.bytes += e.charge();
        self.values.push_back(e);
        out.extend(self.trim(0, now)?);
        Ok(out)
    }
}
pub struct Budget {
    limit: usize,
    used: Mutex<usize>,
}
impl Budget {
    // Initialize the instance and its owned state for bounded cache storage
    pub fn new(limit: usize) -> Result<Self> {
        if limit == 0 || limit > MAX_BYTES {
            return Err(error("invalid byte budget"));
        }
        Ok(Self {
            limit,
            used: Mutex::new(0),
        })
    }
    // Read the currently reserved byte budget
    pub fn used(&self) -> usize {
        *self.used.lock().unwrap()
    }
    // Reserve capacity before accepting additional work
    pub fn reserve(&self, size: usize) -> bool {
        let mut used = self.used.lock().unwrap();
        if size > self.limit - *used {
            return false;
        }
        *used += size;
        true
    }
    // Return reserved capacity to the shared budget
    pub fn release(&self, size: usize) -> Result<()> {
        let mut used = self.used.lock().unwrap();
        if size > *used {
            return Err(error("invalid budget release"));
        }
        *used -= size;
        Ok(())
    }
}
/// Explicit private host storage. Restore is atomic; flush uses bounded commits
/// LRU recency is session-local. Storage capabilities/permissions are required
pub struct Persistence {
    state: ManagedState,
    ready: bool,
    pending: Option<Value>,
}
impl Persistence {
    // Initialize the instance and its owned state for bounded cache storage
    pub fn new(backend: impl PrivateBackend + 'static) -> Self {
        Self {
            state: ManagedState::private(backend),
            ready: false,
            pending: None,
        }
    }
    // Read and validate persisted state before exposing it
    pub fn load<T: Clone + Serialize + DeserializeOwned>(
        &mut self,
        cache: &mut Cache<T>,
        now: u64,
    ) -> Result<()> {
        if self.ready || cache.capacity > 4096 || cache.limit > 8 << 20 || now > MAX_TIME {
            return Err(error("invalid persistent cache state or limits"));
        }
        let records = self.state.load()?;
        let mut temporary = Cache::new(cache.capacity, cache.limit)?;
        for (key, e) in records.as_object().unwrap() {
            let value: T = serde_json::from_value(
                e.get("value")
                    .ok_or_else(|| error("missing cache value"))?
                    .clone(),
            )?;
            let cost = usize::try_from(
                e["cost"]
                    .as_u64()
                    .ok_or_else(|| error("invalid cache cost"))?,
            )?;
            let expires = e["expires"]
                .as_u64()
                .ok_or_else(|| error("invalid cache expiry"))?;
            if cost < serde_json::to_vec(&value)?.len() {
                return Err(error("invalid persistent cache cost"));
            }
            temporary.put(key, value, cost, expires, now)?;
        }
        *cache = temporary;
        self.ready = true;
        Ok(())
    }
    // Write pending state or bytes to the owned destination
    pub fn flush<T: Clone + Serialize>(&mut self, cache: &mut Cache<T>, now: u64) -> Result<()> {
        if !self.ready || now > MAX_TIME {
            return Err(error("cache persistence is not loaded or time is invalid"));
        }
        let mut target = Map::new();
        for e in cache.entries(now)? {
            if e.cost < serde_json::to_vec(&e.value)?.len() {
                return Err(error("invalid persistent cache cost"));
            }
            target.insert(
                e.key,
                json!({"value":e.value,"cost":e.cost,"expires":e.expires}),
            );
        }
        for (identity, value) in &target {
            if serde_json::to_vec(&json!({"identity":identity,"value":value}))?.len() > 256 << 10 {
                return Err(error("persistent cache record exceeds 256 KiB"));
            }
        }
        if let Some(pending) = &self.pending {
            self.state.save(pending)?;
            self.pending = None;
        }
        for _ in 0..8193 {
            let mut written = self.state.load()?;
            let values = written.as_object_mut().unwrap();
            let mut removed: Vec<_> = values
                .keys()
                .filter(|k| !target.contains_key(*k))
                .cloned()
                .collect();
            let mut changed: Vec<_> = target
                .keys()
                .filter(|k| target.get(*k) != values.get(*k))
                .cloned()
                .collect();
            removed.sort();
            changed.sort();
            removed.extend(changed);
            let keys = removed;
            if keys.is_empty() {
                return Ok(());
            }
            let mut batch_bytes = 0;
            for key in keys.into_iter().take(32) {
                let record_bytes = if let Some(v) = target.get(&key) {
                    serde_json::to_vec(&json!({"identity":key,"value":v}))?.len()
                } else {
                    0
                };
                if record_bytes > 256 << 10 {
                    return Err(error("persistent cache record exceeds 256 KiB"));
                }
                let size = record_bytes + 256;
                if batch_bytes + size > 400 << 10 {
                    break;
                }
                batch_bytes += size;
                if let Some(v) = target.get(&key) {
                    values.insert(key, v.clone());
                } else {
                    values.remove(&key);
                }
            }
            self.pending = Some(written.clone());
            self.state.save(&written)?;
            self.pending = None;
        }
        Err(error("cache flush exceeded batch budget"))
    }
}
