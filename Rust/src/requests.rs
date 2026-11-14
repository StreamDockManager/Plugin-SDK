//! Bounded shared JSON reads with independent consumer cancellation and cooldowns
use crate::{error, Result};
use serde_json::Value;
use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Condvar, Mutex,
    },
    time::{Duration, Instant},
};
#[derive(Debug)]
pub struct SharedReadError(pub Arc<crate::Error>);
impl std::fmt::Display for SharedReadError {
    // Format the error for its caller
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        self.0.fmt(f)
    }
}
impl std::error::Error for SharedReadError {
    // Expose the underlying failure for error chaining
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(self.0.as_ref().as_ref())
    }
}
struct FlightData {
    users: usize,
    finished: bool,
    retired: bool,
    result: Option<std::result::Result<Value, Arc<crate::Error>>>,
}
struct Flight {
    cancel: Arc<AtomicBool>,
    deadline: Instant,
    data: Mutex<FlightData>,
    ready: Condvar,
}
#[derive(Default)]
struct State {
    flights: HashMap<String, Arc<Flight>>,
    cooldowns: HashMap<String, (Instant, Arc<crate::Error>)>,
    slots: usize,
    closed: bool,
}
#[derive(Clone, Default)]
pub struct RequestGroup {
    state: Arc<Mutex<State>>,
}
impl RequestGroup {
    /// Work returns a result and a service cooldown. Only failed reads set cooldowns
    pub fn get(
        &self,
        key: &str,
        cancel: &AtomicBool,
        work: impl FnOnce(Arc<AtomicBool>) -> (Result<Value>, Duration) + Send + 'static,
    ) -> Result<Value> {
        if cancel.load(Ordering::Acquire) {
            return Err(error("shared read cancelled"));
        }
        if key.is_empty() || key.len() > 2048 {
            return Err(error("invalid shared read key"));
        }
        let mut state = self.state.lock().unwrap();
        if state.closed {
            return Err(error("shared read group closed"));
        }
        let now = Instant::now();
        state.cooldowns.retain(|_, (until, _)| *until > now);
        if let Some((_, error)) = state.cooldowns.get(key) {
            return Err(Box::new(SharedReadError(error.clone())));
        }
        let flight = if let Some(flight) = state.flights.get(key) {
            flight.clone()
        } else {
            if state.slots + state.cooldowns.len() >= 32 {
                return Err(error("shared read capacity exhausted"));
            }
            let flight = Arc::new(Flight {
                cancel: Arc::new(AtomicBool::new(false)),
                deadline: now + Duration::from_secs(8),
                data: Mutex::new(FlightData {
                    users: 0,
                    finished: false,
                    retired: false,
                    result: None,
                }),
                ready: Condvar::new(),
            });
            state.slots += 1;
            state.flights.insert(key.into(), flight.clone());
            let owner = flight.clone();
            let shared = self.state.clone();
            let key_owned = key.to_string();
            let spawned = std::thread::Builder::new()
                .name("sdk-shared-read".into())
                // Share one bounded read result among waiters and retain its slot until work actually ends
                .spawn(move || {
                    let (mut result, delay) =
                        std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                            work(owner.cancel.clone())
                        }))
                        .unwrap_or_else(|_| (Err(error("shared read panicked")), Duration::ZERO));
                    if let Ok(value) = &result {
                        if serde_json::to_vec(value).map_or(true, |data| data.len() > 1 << 20) {
                            result = Err(error("shared read exceeds 1 MiB"))
                        }
                    }
                    if Instant::now() >= owner.deadline || owner.cancel.load(Ordering::Acquire) {
                        result = Err(error("shared read cancelled or timed out"))
                    }
                    let result = result.map_err(Arc::new);
                    let mut state = shared.lock().unwrap();
                    let mut data = owner.data.lock().unwrap();
                    data.finished = true;
                    // Retired work continues occupying capacity until the worker has finished
                    if data.users == 0 {
                        state.slots -= 1
                    }
                    if state
                        .flights
                        .get(&key_owned)
                        .is_some_and(|current| Arc::ptr_eq(current, &owner))
                        && !state.closed
                        && !owner.cancel.load(Ordering::Acquire)
                        && Instant::now() < owner.deadline
                        && !delay.is_zero()
                    {
                        if let Err(error) = &result {
                            state.cooldowns.insert(
                                key_owned,
                                (
                                    Instant::now()
                                        + delay.min(Duration::from_nanos(i64::MAX as u64)),
                                    error.clone(),
                                ),
                            );
                        }
                    }
                    data.result = Some(result);
                    owner.ready.notify_all();
                });
            if let Err(error) = spawned {
                state.flights.remove(key);
                state.slots -= 1;
                return Err(error.into());
            }
            flight
        };
        flight.data.lock().unwrap().users += 1;
        drop(state);
        let outcome = {
            let mut data = flight.data.lock().unwrap();
            loop {
                if Instant::now() >= flight.deadline {
                    flight.cancel.store(true, Ordering::Release)
                }
                if cancel.load(Ordering::Acquire)
                    || flight.cancel.load(Ordering::Acquire)
                    || data.retired
                {
                    break Err(error("shared read cancelled or timed out"));
                }
                if let Some(result) = &data.result {
                    break result
                        .clone()
                        .map_err(|e| Box::new(SharedReadError(e)) as crate::Error);
                }
                data = flight
                    .ready
                    .wait_timeout(data, Duration::from_millis(10))
                    .unwrap()
                    .0;
            }
        };
        let mut state = self.state.lock().unwrap();
        let mut data = flight.data.lock().unwrap();
        data.users -= 1;
        if data.users == 0 {
            if state
                .flights
                .get(key)
                .is_some_and(|current| Arc::ptr_eq(current, &flight))
            {
                state.flights.remove(key);
            }
            flight.cancel.store(true, Ordering::Release);
            if data.finished {
                state.slots -= 1
            }
        }
        outcome
    }
    // Cancel pending shared work without restarting it
    pub fn cancel_pending(&self) {
        let mut state = self.state.lock().unwrap();
        Self::cancel(&mut state)
    }
    // Cancel the selected pending operation
    fn cancel(state: &mut State) {
        for (_, flight) in state.flights.drain() {
            flight.data.lock().unwrap().retired = true;
            flight.cancel.store(true, Ordering::Release);
            flight.ready.notify_all()
        }
    }
    // Close the owned session and release pending work for shared read requests
    pub fn close(&self) {
        let mut state = self.state.lock().unwrap();
        state.closed = true;
        Self::cancel(&mut state);
        state.cooldowns.clear()
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    // Construct the shared-read request specification
    fn spec() -> Value {
        serde_json::from_str::<Value>(include_str!("../testdata/extensions-v1.json")).unwrap()
            ["shared_reads"]
            .clone()
    }
    // Wait for the operation result or cancellation
    fn wait(ready: impl Fn() -> bool) {
        let deadline = Instant::now() + Duration::from_secs(2);
        while !ready() {
            assert!(Instant::now() < deadline, "condition did not complete");
            std::thread::sleep(Duration::from_millis(1))
        }
    }
    #[test]
    // Verify cooldown expires and read resumes
    fn cooldown_expires_and_read_resumes() {
        let group = RequestGroup::default();
        let cancel = AtomicBool::new(false);
        assert!(group
            .get("expiry", &cancel, |_| (
                Err(error("cooldown")),
                Duration::from_millis(20)
            ))
            .is_err());
        std::thread::sleep(Duration::from_millis(30));
        assert_eq!(
            group
                .get("expiry", &cancel, |_| (
                    Ok(serde_json::json!(7)),
                    Duration::ZERO
                ))
                .unwrap(),
            serde_json::json!(7)
        );
    }
    #[test]
    // Verify shared reads cancellation cooldown and retirement
    fn shared_reads_cancellation_cooldown_and_retirement() {
        let spec = spec();
        let group = RequestGroup::default();
        let release = Arc::new(AtomicBool::new(false));
        let count = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let shared = Arc::new(Mutex::new(None::<Arc<AtomicBool>>));
        let cancel = Arc::new(AtomicBool::new(false));
        let mut threads = Vec::new();
        for index in 0..2 {
            let g = group.clone();
            let release = release.clone();
            let count = count.clone();
            let shared = shared.clone();
            let stop = if index == 0 {
                cancel.clone()
            } else {
                Arc::new(AtomicBool::new(false))
            };
            let value = spec["value"].clone();
            let key = spec["key"].as_str().unwrap().to_string();
            threads.push(std::thread::spawn(move || {
                g.get(&key, &stop, move |flag| {
                    count.fetch_add(1, Ordering::Relaxed);
                    *shared.lock().unwrap() = Some(flag);
                    wait(|| release.load(Ordering::Acquire));
                    (Ok(value), Duration::ZERO)
                })
            }))
        }
        wait(|| {
            group
                .state
                .lock()
                .unwrap()
                .flights
                .get(spec["key"].as_str().unwrap())
                .is_some_and(|f| f.data.lock().unwrap().users == 2)
        });
        wait(|| shared.lock().unwrap().is_some());
        cancel.store(true, Ordering::Release);
        assert!(threads.remove(0).join().unwrap().is_err());
        assert!(!shared
            .lock()
            .unwrap()
            .as_ref()
            .unwrap()
            .load(Ordering::Acquire));
        release.store(true, Ordering::Release);
        assert_eq!(threads.remove(0).join().unwrap().unwrap(), spec["value"]);
        assert_eq!(count.load(Ordering::Relaxed), 1);
        assert!(group
            .get("cooldown", &AtomicBool::new(false), |_| (
                Err(error("service cooldown")),
                Duration::from_secs(60)
            ))
            .is_err());
        assert!(group
            .get("cooldown", &AtomicBool::new(false), |_| panic!(
                "ignored cooldown"
            ))
            .is_err());
        let old_release = Arc::new(AtomicBool::new(false));
        let started = Arc::new(AtomicBool::new(false));
        let stop = Arc::new(AtomicBool::new(false));
        let g = group.clone();
        let flag = stop.clone();
        let done = old_release.clone();
        let begin = started.clone();
        let old = std::thread::spawn(move || {
            g.get("retired", &flag, move |_| {
                begin.store(true, Ordering::Release);
                wait(|| done.load(Ordering::Acquire));
                (Err(error("late cooldown")), Duration::from_secs(60))
            })
        });
        wait(|| started.load(Ordering::Acquire));
        stop.store(true, Ordering::Release);
        assert!(old.join().unwrap().is_err());
        assert_eq!(
            group
                .get("retired", &AtomicBool::new(false), |_| (
                    Ok(json!(1)),
                    Duration::ZERO
                ))
                .unwrap(),
            1
        );
        old_release.store(true, Ordering::Release);
        wait(|| group.state.lock().unwrap().slots == 0);
        assert_eq!(
            group
                .get("retired", &AtomicBool::new(false), |_| (
                    Ok(json!(1)),
                    Duration::ZERO
                ))
                .unwrap(),
            1
        );
        assert!(group
            .get("oversized", &AtomicBool::new(false), |_| (
                Ok(json!("x".repeat((1 << 20) + 1))),
                Duration::ZERO
            ))
            .is_err());
        group.close();
        assert!(group
            .get("closed", &AtomicBool::new(false), |_| panic!())
            .is_err());
    }
    #[test]
    // Verify retired work retains capacity until finished
    fn retired_work_retains_capacity_until_finished() {
        let spec = spec();
        let group = RequestGroup::default();
        let release = Arc::new(AtomicBool::new(false));
        let mut pending = Vec::new();
        let capacity = spec["capacity"].as_u64().unwrap() as usize;
        for i in 0..capacity {
            let g = group.clone();
            let done = release.clone();
            let stop = Arc::new(AtomicBool::new(false));
            let flag = stop.clone();
            let thread = std::thread::spawn(move || {
                g.get(&i.to_string(), &flag, move |_| {
                    wait(|| done.load(Ordering::Acquire));
                    (Ok(json!(1)), Duration::ZERO)
                })
            });
            pending.push((stop, thread));
        }
        wait(|| group.state.lock().unwrap().flights.len() == capacity);
        for (stop, thread) in pending {
            stop.store(true, Ordering::Release);
            assert!(thread.join().unwrap().is_err());
        }
        assert!(group
            .get("overflow", &AtomicBool::new(false), |_| (
                Ok(json!(1)),
                Duration::ZERO
            ))
            .is_err());
        release.store(true, Ordering::Release);
        wait(|| group.state.lock().unwrap().slots == 0);
        assert_eq!(
            group
                .get("reused", &AtomicBool::new(false), |_| (
                    Ok(json!(1)),
                    Duration::ZERO
                ))
                .unwrap(),
            1
        );
        group.close();
    }
}
