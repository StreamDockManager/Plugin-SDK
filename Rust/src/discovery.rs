//! Read-only request coordinator. Schedule on the SDK bounded executor, complete
//! and expire on the owner loop, and clear on unbind. Never cancels command work
use crate::{error, Result};
use serde_json::{json, Map, Value};
use std::{
    collections::BTreeMap,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
};
pub const MAX_PENDING: usize = 32;
pub const MAX_RESPONSE_BYTES: usize = 256 << 10;
const MAX_TIME: u64 = 9007199254740991;
#[derive(Clone, Default)]
pub struct Request {
    pub context: String,
    pub action: String,
    pub channel: String,
    pub id: String,
    pub operation: String,
    pub scope: String,
}
#[derive(Clone)]
pub struct Ticket {
    pub request: Request,
    pub deadline: u64,
    pub cancel: Arc<AtomicBool>,
}
pub struct Reply {
    pub context: String,
    pub action: String,
    pub payload: Value,
}
#[derive(Default)]
pub struct Runner {
    pending: BTreeMap<(String, String), Ticket>,
    closed: bool,
}
// Build a discovery field description
fn field(s: &str, max: usize, empty: bool) -> bool {
    (empty || !s.is_empty()) && s.len() <= max
}
// Reject values outside the supported contract for inspector discovery
fn validate(r: &Request) -> Result<()> {
    if !field(&r.context, 1024, false)
        || !field(&r.action, 1024, true)
        || !field(&r.channel, 128, false)
        || !field(&r.id, 128, false)
        || !field(&r.operation, 128, false)
        || !matches!(r.scope.as_str(), "" | "action" | "plugin")
    {
        return Err(error("invalid discovery request"));
    }
    Ok(())
}
// Wrap the discovery result in its protocol envelope
pub fn envelope(request: &Request, payload: Map<String, Value>, failure: &str) -> Result<Reply> {
    validate(request)?;
    let mut result = payload;
    result.insert("op".into(), json!(request.operation));
    result.insert("request_id".into(), json!(request.id));
    result.insert(
        "scope".into(),
        json!(if request.scope.is_empty() {
            "action"
        } else {
            &request.scope
        }),
    );
    result.insert("available".into(), json!(failure.is_empty()));
    if !failure.is_empty() {
        result.insert("error".into(), json!(failure));
        result.entry("message").or_insert_with(|| {
            json!(match failure {
                "timeout" => "Discovery timed out",
                "busy" => "Discovery is busy; try again shortly",
                _ => "Discovery failed",
            })
        });
    }
    if serde_json::to_vec(&result)?.len() > MAX_RESPONSE_BYTES {
        return envelope(
            request,
            Map::from_iter([(
                "message".into(),
                json!("Discovery response exceeds its supported size"),
            )]),
            "response_too_large",
        );
    }
    Ok(Reply {
        context: request.context.clone(),
        action: request.action.clone(),
        payload: Value::Object(result),
    })
}
impl Runner {
    // Start the requested observation or operation for inspector discovery
    pub fn start(&mut self, mut request: Request, now: u64, timeout: u64) -> Result<Ticket> {
        validate(&request)?;
        if self.closed
            || now > MAX_TIME
            || timeout == 0
            || timeout > 120000
            || now > MAX_TIME - timeout
        {
            return Err(error("invalid discovery deadline or closed runner"));
        }
        let key = (request.context.clone(), request.channel.clone());
        if !self.pending.contains_key(&key) && self.pending.len() >= MAX_PENDING {
            return Err(error("discovery queue full"));
        }
        if let Some(old) = self.pending.get(&key) {
            old.cancel.store(true, Ordering::Release);
        }
        if request.scope.is_empty() {
            request.scope = "action".into();
        }
        let ticket = Ticket {
            request,
            deadline: now + timeout,
            cancel: Arc::new(AtomicBool::new(false)),
        };
        self.pending.insert(key, ticket.clone());
        Ok(ticket)
    }
    // Cancel the selected pending operation
    pub fn cancel(&mut self, context: &str, channel: &str, id: &str) -> bool {
        let key = (context.into(), channel.into());
        if !self.pending.get(&key).is_some_and(|t| t.request.id == id) {
            return false;
        }
        self.pending
            .remove(&key)
            .unwrap()
            .cancel
            .store(true, Ordering::Release);
        true
    }
    // Cancel and remove discovery tickets owned by this context
    pub fn clear(&mut self, context: &str) {
        // Cancel only tickets belonging to the removed context and retain other consumers
        self.pending.retain(|(owner, _), t| {
            if owner == context {
                t.cancel.store(true, Ordering::Release);
                false
            } else {
                true
            }
        });
    }
    // Close the owned session and release pending work for inspector discovery
    pub fn close(&mut self) {
        for t in self.pending.values() {
            t.cancel.store(true, Ordering::Release);
        }
        self.pending.clear();
        self.closed = true;
    }
    // Return the earliest outstanding discovery deadline
    pub fn next_deadline(&self) -> u64 {
        self.pending.values().map(|t| t.deadline).min().unwrap_or(0)
    }
    // Record completion and release its pending state for inspector discovery
    pub fn complete(
        &mut self,
        ticket: &Ticket,
        mut payload: Map<String, Value>,
        mut failure: &str,
        now: u64,
    ) -> Result<Option<Reply>> {
        if now > MAX_TIME {
            return Err(error("invalid discovery time"));
        }
        let key = (
            ticket.request.context.clone(),
            ticket.request.channel.clone(),
        );
        if !self
            .pending
            .get(&key)
            .is_some_and(|t| Arc::ptr_eq(&t.cancel, &ticket.cancel))
        {
            return Ok(None);
        }
        if now >= ticket.deadline {
            payload.clear();
            failure = "timeout";
        } else if ticket.cancel.load(Ordering::Acquire) {
            self.pending.remove(&key);
            return Ok(None);
        }
        let reply = envelope(&ticket.request, payload, failure)?;
        self.pending.remove(&key);
        ticket.cancel.store(true, Ordering::Release);
        Ok(Some(reply))
    }
    // Complete discovery requests whose deadlines elapsed
    pub fn expire(&mut self, now: u64) -> Result<Vec<Reply>> {
        if now > MAX_TIME {
            return Err(error("invalid discovery time"));
        }
        let mut tickets: Vec<_> = self
            .pending
            .values()
            .filter(|t| t.deadline <= now)
            .cloned()
            .collect();
        tickets.sort_by(|a, b| {
            (a.deadline, &a.request.context, &a.request.channel).cmp(&(
                b.deadline,
                &b.request.context,
                &b.request.channel,
            ))
        });
        let mut replies = Vec::new();
        for t in tickets {
            if let Some(reply) = self.complete(&t, Map::new(), "timeout", now)? {
                replies.push(reply)
            }
        }
        Ok(replies)
    }
}
