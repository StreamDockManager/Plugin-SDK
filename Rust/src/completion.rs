use crate::{error, Result};
use std::sync::{Arc, Mutex};
#[derive(Clone)]
pub struct Completion(Arc<Inner>);
struct Inner {
    state: Mutex<State>,
    emit: Box<dyn Fn(bool) + Send + Sync>,
}
#[derive(Default)]
struct State {
    pending: usize,
    returned: bool,
    failed: bool,
    sent: bool,
}
impl Completion {
    // Initialize the instance and its owned state for deferred action completion
    pub fn new(emit: impl Fn(bool) + Send + Sync + 'static) -> Self {
        Self(Arc::new(Inner {
            state: Mutex::new(State::default()),
            emit: Box::new(emit),
        }))
    }
    // Reserve a deferred completion receipt
    pub fn defer(&self) -> Deferred {
        let mut s = self.0.state.lock().unwrap();
        assert!(!s.sent, "action already completed");
        s.pending += 1;
        Deferred {
            action: Some(self.clone()),
        }
    }
    // Complete the operation exactly once for deferred action completion
    pub fn finish(&self, result: Result<()>, returned: bool) {
        let mut s = self.0.state.lock().unwrap();
        s.failed |= result.is_err();
        if returned {
            s.returned = true
        } else {
            s.pending -= 1
        }
        let emit = s.returned && s.pending == 0 && !s.sent;
        let success = !s.failed;
        if emit {
            s.sent = true
        }
        drop(s);
        if emit {
            (self.0.emit)(success)
        }
    }
    // Cancel the selected pending operation
    pub fn cancel(&self) {
        let mut s = self.0.state.lock().unwrap();
        let emit = !s.sent;
        s.sent = true;
        drop(s);
        if emit {
            (self.0.emit)(false)
        }
    }
}
/// Dropping an unfinished deferred operation reports failure, never success
pub struct Deferred {
    pub(crate) action: Option<Completion>,
}
impl Deferred {
    // Complete the operation exactly once for deferred action completion
    pub fn finish(mut self, result: Result<()>) {
        if let Some(a) = self.action.take() {
            a.finish(result, false)
        }
    }
}
impl Drop for Deferred {
    // Complete an abandoned deferred action as cancelled rather than leaving its receipt pending
    fn drop(&mut self) {
        if let Some(a) = self.action.take() {
            a.finish(Err(error("deferred action cancelled")), false)
        }
    }
}
