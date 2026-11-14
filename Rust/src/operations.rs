//! Owner-serialized operation history. Never submits or retries external work
use crate::{error, Result};
use std::collections::{BTreeMap, BTreeSet};
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum State {
    Pending,
    Confirmed,
    Failed,
    Uncertain,
}
#[derive(Clone, Debug)]
pub struct Record<T> {
    pub id: String,
    pub group: String,
    pub sequence: u64,
    pub state: State,
    pub pinned: bool,
    pub value: T,
    pub cost: usize,
}
impl<T> Record<T> {
    // Calculate the logical memory charge including bookkeeping
    fn charge(&self) -> usize {
        self.id.len() + self.group.len() + self.cost + 64
    }
}
pub struct Ledger<T> {
    records: BTreeMap<String, Record<T>>,
    capacity: usize,
    limit: usize,
    bytes: usize,
    sequence: u64,
}
impl<T: Clone> Ledger<T> {
    // Initialize the instance and its owned state for operation history
    pub fn new(capacity: usize, limit: usize) -> Result<Self> {
        if capacity == 0 || capacity > 65536 || limit == 0 || limit > 64 << 20 {
            return Err(error("invalid operation history limits"));
        }
        Ok(Self {
            records: BTreeMap::new(),
            capacity,
            limit,
            bytes: 0,
            sequence: 0,
        })
    }
    // Return the number of retained entries
    pub fn len(&self) -> usize {
        self.records.len()
    }
    // Report whether no entries are retained
    pub fn is_empty(&self) -> bool {
        self.records.is_empty()
    }
    // Return the logical byte charge of retained entries
    pub fn bytes(&self) -> usize {
        self.bytes
    }
    // Read the requested value through the current owner for operation history
    pub fn get(&self, id: &str) -> Option<Record<T>> {
        self.records.get(id).cloned()
    }
    // Copy the retained operation records
    pub fn records(&self, group: &str) -> Vec<Record<T>> {
        let mut out: Vec<_> = self
            .records
            .values()
            .filter(|r| group.is_empty() || r.group == group)
            .cloned()
            .collect();
        out.sort_by_key(|r| r.sequence);
        out
    }
    // Reserve capacity before accepting additional work
    pub fn reserve(&mut self, group: &str, value: T, cost: usize) -> Result<Record<T>> {
        if group.is_empty()
            || group.len() > 4096
            || cost > self.limit
            || self.len() >= self.capacity
            || self.sequence >= 9007199254740991
        {
            return Err(error("invalid operation or history full"));
        }
        let mut sequence = self.sequence + 1;
        let mut id = group.to_owned();
        if self.records.contains_key(&id) {
            loop {
                id = format!("{group}#{sequence}");
                if !self.records.contains_key(&id) {
                    break;
                }
                sequence += 1;
                if sequence > 9007199254740991 {
                    return Err(error("operation sequence exhausted"));
                }
            }
        }
        let record = Record {
            id: id.clone(),
            group: group.into(),
            sequence,
            state: State::Pending,
            pinned: false,
            value,
            cost,
        };
        if record.charge() > self.limit - self.bytes {
            return Err(error("operation history byte budget exceeded"));
        }
        self.sequence = sequence;
        self.bytes += record.charge();
        self.records.insert(id, record.clone());
        Ok(record)
    }
    // Apply the requested change while preserving owner state for operation history
    pub fn update(
        &mut self,
        id: &str,
        state: State,
        value: T,
        cost: usize,
        pinned: bool,
    ) -> Result<()> {
        let prior = self
            .records
            .get_mut(id)
            .ok_or_else(|| error("unknown operation"))?;
        if cost > self.limit || cost > self.limit - self.bytes + prior.cost {
            return Err(error("operation history byte budget exceeded"));
        }
        self.bytes = self.bytes - prior.cost + cost;
        prior.state = state;
        prior.value = value;
        prior.cost = cost;
        prior.pinned = pinned;
        Ok(())
    }
    // Remove completed records according to retention policy
    pub fn retire(&mut self, active: &BTreeSet<String>) -> Vec<String> {
        let mut out = Vec::new();
        for r in self.records("") {
            if matches!(r.state, State::Confirmed | State::Failed)
                && !r.pinned
                && !active.contains(&r.group)
            {
                self.records.remove(&r.id);
                self.bytes -= r.charge();
                out.push(r.id);
            }
        }
        out
    }
}
