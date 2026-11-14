//! Resource polling is demand-driven and clock-explicit; this module performs no I/O
use crate::{error, Result};
use serde_json::Value;
use std::collections::BTreeMap;
pub const UPDATE_INTERVAL_KEY: &str = "update_interval_ms";
pub const NEVER: u64 = u64::MAX;
#[derive(Clone, Debug, Default, PartialEq, serde::Serialize, serde::Deserialize)]
pub struct RefreshPolicy {
    pub default_ms: u64,
    pub minimum_ms: u64,
    pub maximum_ms: u64,
}
impl RefreshPolicy {
    // Resolve an override against the configured defaults
    pub fn resolve(&self, settings: &Value) -> Result<u64> {
        if self.minimum_ms == 0
            || self.maximum_ms < self.minimum_ms
            || self.maximum_ms > 86_400_000
            || (self.default_ms != 0
                && !(self.minimum_ms..=self.maximum_ms).contains(&self.default_ms))
        {
            return Err(error("invalid refresh policy"));
        }
        let value = match settings.get(UPDATE_INTERVAL_KEY) {
            None => self.default_ms,
            Some(value) => {
                let n = value
                    .as_f64()
                    .ok_or_else(|| error("invalid update interval"))?;
                if !n.is_finite() || n < 0. || n.fract() != 0. || n > 86_400_000. {
                    return Err(error("invalid update interval"));
                }
                n as u64
            }
        };
        if value != 0 && !(self.minimum_ms..=self.maximum_ms).contains(&value) {
            return Err(error("update interval is outside the supported range"));
        }
        Ok(value)
    }
}
// Choose the configured polling interval
pub fn update_interval(settings: &Value, fallback: u64) -> u64 {
    RefreshPolicy {
        default_ms: fallback,
        minimum_ms: 1,
        maximum_ms: 86_400_000,
    }
    .resolve(settings)
    .unwrap_or(fallback)
}
// Compute the next update deadline
pub fn next_update(now: u64, interval: u64) -> u64 {
    if interval == 0 {
        NEVER
    } else {
        now.saturating_add(interval)
    }
}
// Compute the bounded delay before another attempt
pub fn retry_delay(interval: u64, failures: u32, jitter: f64) -> u64 {
    let backoff = (2000u64 * (1 << failures.saturating_sub(1).min(10))).min(900_000);
    interval.max((backoff as f64 * (1.0 + 0.25 * jitter.clamp(0., 1.))).min(900_000.) as u64)
}
#[derive(Clone, Debug, Default)]
pub struct PollState {
    pub next: u64,
    pub failures: u32,
    pub last_value: Value,
    pub stale: bool,
    pub observed_at: Option<u64>,
}
impl PollState {
    // Record completion and release its pending state for poll scheduling
    pub fn complete(
        &mut self,
        value: Value,
        failed: bool,
        now: u64,
        interval: u64,
        jitter: f64,
        retry_at: Option<u64>,
    ) {
        if failed {
            self.failures = (self.failures + 1).min(16);
            self.stale = self.observed_at.is_some();
            self.next = now.saturating_add(retry_delay(interval, self.failures, jitter));
        } else {
            self.failures = 0;
            self.stale = false;
            self.last_value = value;
            self.observed_at = Some(now);
            self.next = next_update(now, interval)
        }
        if let Some(retry) = retry_at {
            self.next = self.next.max(retry)
        }
    }
}
#[derive(Default)]
pub struct PollResult {
    pub value: Value,
    pub failed: bool,
    pub now: u64,
    pub jitter: f64,
    pub retry_at: Option<u64>,
}
#[derive(Default)]
pub struct Resources {
    bindings: BTreeMap<String, (String, u64)>,
    delivered: BTreeMap<String, PollState>,
    pub states: BTreeMap<String, PollState>,
    pending: BTreeMap<String, u64>,
    cooldowns: BTreeMap<String, u64>,
    sequence: u64,
}
impl Resources {
    // Register a consumer for shared observations
    pub fn watch(&mut self, context: &str, key: &str, interval: u64) -> Result<()> {
        if key.is_empty() {
            return Err(error("resource needs key"));
        };
        if self
            .bindings
            .get(context)
            .is_some_and(|(prior, _)| prior != key)
        {
            self.unwatch(context)
        };
        let changed = self
            .bindings
            .get(context)
            .is_none_or(|(_, old)| *old != interval);
        let state = self
            .delivered
            .entry(context.into())
            .or_insert_with(|| self.states.get(key).cloned().unwrap_or_default());
        if changed {
            if let Some(at) = state.observed_at {
                state.next = next_update(at, interval);
            }
        }
        self.bindings.insert(context.into(), (key.into(), interval));
        self.states.entry(key.into()).or_default();
        if changed && self.states[key].next != 0 {
            self.schedule(key);
        }
        Ok(())
    }
    // Remove a consumer from shared observations
    pub fn unwatch(&mut self, context: &str) {
        self.delivered.remove(context);
        if let Some((prior, _)) = self.bindings.remove(context) {
            if !self.bindings.values().any(|(key, _)| key == &prior) {
                self.states.remove(&prior);
                self.pending.remove(&prior);
                self.cooldowns.remove(&prior);
            }
            if self.states.get(&prior).is_some_and(|state| state.next != 0) {
                self.schedule(&prior);
            }
        }
    }
    // Select observations whose deadlines have arrived
    pub fn due(&self, now: u64) -> Vec<String> {
        self.states
            .iter()
            .filter(|(key, state)| {
                !self.pending.contains_key(*key)
                    && now >= state.next
                    && now >= *self.cooldowns.get(*key).unwrap_or(&0)
            })
            .map(|(key, _)| key.clone())
            .collect()
    }
    // Return the next scheduled deadline
    pub fn deadline(&self, key: &str) -> u64 {
        self.states
            .get(key)
            .map(|state| state.next.max(*self.cooldowns.get(key).unwrap_or(&0)))
            .unwrap_or(NEVER)
    }
    // Record the start of an operation for poll scheduling
    pub fn begin(&mut self, key: &str) -> Result<u64> {
        if !self.states.contains_key(key) || self.pending.contains_key(key) {
            return Err(error("resource absent or already running"));
        };
        self.sequence = self
            .sequence
            .checked_add(1)
            .ok_or_else(|| error("resource generation exhausted"))?;
        let value = self.sequence;
        self.pending.insert(key.into(), value);
        Ok(value)
    }
    // Record completion and release its pending state for poll scheduling
    pub fn complete(&mut self, key: &str, generation: u64, result: PollResult) -> bool {
        if self.pending.get(key) != Some(&generation) || !self.states.contains_key(key) {
            return false;
        };
        self.pending.remove(key);
        if result.retry_at.is_some_and(|at| at > result.now) {
            self.cooldowns.insert(key.into(), result.retry_at.unwrap());
        } else {
            self.cooldowns.remove(key);
        }
        let forced = self.states[key].next == 0;
        for (id, (resource, interval)) in &self.bindings {
            let state = self.delivered.get_mut(id).unwrap();
            if resource != key || (!forced && result.now < state.next) {
                continue;
            }
            state.complete(
                result.value.clone(),
                result.failed,
                result.now,
                *interval,
                result.jitter,
                result.retry_at,
            );
            if *interval == 0 {
                state.next = NEVER;
            }
        }
        self.states.get_mut(key).unwrap().complete(
            result.value,
            result.failed,
            result.now,
            0,
            result.jitter,
            result.retry_at,
        );
        self.schedule(key);
        true
    }
    // Copy the current observable state for poll scheduling
    pub fn snapshot(&self, context: &str) -> Option<PollState> {
        self.delivered.get(context).cloned()
    }
    // Compute the next eligible polling deadline
    fn schedule(&mut self, key: &str) {
        if let Some(state) = self.states.get_mut(key) {
            state.next = self
                .bindings
                .iter()
                .filter(|(_, (resource, _))| resource == key)
                .map(|(id, _)| self.delivered[id].next)
                .min()
                .unwrap_or(NEVER);
            state.next = state.next.max(*self.cooldowns.get(key).unwrap_or(&0));
        }
    }
}
