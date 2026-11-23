//! Serialized, demand-driven service integrations. Drivers own service semantics
use crate::{
    authoring::{Display, Plugin},
    error,
    polling::{PollResult, Resources},
    Client, Deferred, Event, Handler, ProbePool, Result,
};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::{
    collections::{BTreeMap, VecDeque},
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc::{sync_channel, Receiver, SyncSender},
        Arc, Mutex,
    },
};
#[derive(Clone, Default, Serialize, Deserialize)]
pub struct Choice {
    pub id: String,
    pub name: String,
    pub kind: String,
}
#[derive(Clone, Default, Serialize, Deserialize)]
pub struct Outcome {
    pub message: String,
    pub value: Value,
}
pub trait Driver: Send + 'static {
    // Derive the identity used to share or store state for connector observations
    fn key(&self, settings: &Value) -> String;
    // Read data through the owned transport or state backend for connector observations
    fn read(&mut self, cancel: Arc<AtomicBool>, settings: &Value) -> Result<Value>;
    // Apply the requested connector command
    fn command(
        &mut self,
        cancel: Arc<AtomicBool>,
        action: &str,
        settings: &Value,
        ticks: i32,
    ) -> Result<Outcome>;
    // Resolve the requested connector discovery values
    fn discover(&mut self, cancel: Arc<AtomicBool>, settings: &Value) -> Result<Vec<Choice>>;
    // Produce the current control display
    fn display(&self, value: &Value, action: &str, settings: &Value) -> String;
    // Close the owned session and release pending work for connector observations
    fn close(&mut self);
    // Describe the observation required by this action
    fn demand(&mut self, _bindings: &[Value]) {}
    // Refresh state after an observed change
    fn changed(&mut self) -> bool {
        false
    }
    // Read the current display-facing observation state
    fn visual_state(&self, _value: &Value, _action: &str) -> u32 {
        0
    }
}
pub struct Options {
    pub read_timeout_ms: u64,
    pub command_timeout_ms: u64,
    pub authorize_timeout_ms: u64,
    pub interval_ms: u64,
    pub shared_keys: Vec<String>,
    pub dial_actions: Vec<String>,
}
impl Default for Options {
    // Supply the default configuration without performing I/O for connector observations
    fn default() -> Self {
        Self {
            interval_ms: 1000,
            read_timeout_ms: 12000,
            command_timeout_ms: 15000,
            authorize_timeout_ms: 120000,
            shared_keys: vec![],
            dial_actions: vec![],
        }
    }
}
struct Request {
    request_id: Option<String>,
    scope: String,
    context: String,
    action: String,
    op: String,
    settings: Value,
    ticks: i32,
    receipt: Option<Deferred>,
    resource: Option<(String, u64)>,
}
struct Active {
    keep_connection: Arc<AtomicBool>,
    deadline: std::time::Instant,
    request: Request,
    generation: u64,
    cancel: Arc<AtomicBool>,
}
struct Completed {
    generation: u64,
    value: Value,
    failure: Option<String>,
    retry_at: Option<u64>,
}
pub struct Connector {
    pub plugin: Plugin,
    pub options: Options,
    driver: Arc<Mutex<Box<dyn Driver>>>,
    resources: Resources,
    queue: VecDeque<Request>,
    active: Option<Active>,
    sequence: u64,
    messages: BTreeMap<String, String>,
    pool: ProbePool,
    sender: SyncSender<Completed>,
    receiver: Receiver<Completed>,
}
impl Connector {
    // Report whether a connector command is in progress
    pub fn is_busy(&self) -> bool {
        self.active.is_some()
    }
    // Initialize the instance and its owned state for connector observations
    pub fn new(driver: impl Driver, options: Options) -> Self {
        let (sender, receiver) = sync_channel(36);
        Self {
            plugin: Plugin::default(),
            options,
            driver: Arc::new(Mutex::new(Box::new(driver))),
            resources: Resources::default(),
            queue: VecDeque::new(),
            active: None,
            sequence: 0,
            messages: BTreeMap::new(),
            pool: ProbePool::default(),
            sender,
            receiver,
        }
    }
    // Apply or retrieve the action settings in this scope for connector observations
    fn settings(&self, context: &str) -> Value {
        let action = self
            .plugin
            .bindings
            .get(context)
            .map(|b| &b.settings)
            .unwrap_or(&Value::Null);
        let mut s =
            crate::settings::project(&self.plugin.globals, action, &self.options.shared_keys);
        s["_action"] = json!(self
            .plugin
            .bindings
            .get(context)
            .map(|b| b.action.rsplit('.').next().unwrap_or(""))
            .unwrap_or(""));
        s
    }
    // Register a consumer for shared observations
    fn watch(&mut self) -> Result<()> {
        let demand: Vec<_> = self
            .plugin
            .bindings
            .keys()
            .map(|id| (id.clone(), self.settings(id)))
            .collect();
        if let Ok(mut driver) = self.driver.try_lock() {
            for (id, s) in &demand {
                self.resources.watch(
                    id,
                    &driver.key(s),
                    crate::polling::update_interval(s, self.options.interval_ms),
                )?;
            }
            driver.demand(&demand.into_iter().map(|(_, s)| s).collect::<Vec<_>>());
        }
        Ok(())
    }
    fn cancel_binding(&mut self, id: &str) {
        self.messages.remove(id);
        self.queue
            .retain(|q| q.context != id || q.resource.is_some());
        if self
            .active
            .as_ref()
            .is_some_and(|a| a.request.context == id && a.request.resource.is_none())
        {
            let active = self.active.take().unwrap();
            active.keep_connection.store(true, Ordering::Release);
            active.cancel.store(true, Ordering::Release);
        }
    }
    // Reset retained state for the next lifecycle for connector observations
    fn reset(&mut self) {
        if let Some(active) = self.active.take() {
            active.cancel.store(true, Ordering::Release);
        }
        self.queue.clear();
        self.messages.clear();
        self.resources = Resources::default();
        if let Ok(mut driver) = self.driver.try_lock() {
            driver.close();
        }
    }
    // Produce the current display representation for connector observations
    fn render(&mut self, client: &Client) -> Result<()> {
        if let Ok(driver) = self.driver.try_lock() {
            for (id, binding) in &self.plugin.bindings {
                let state = self.resources.snapshot(id);
                let text = self.messages.get(id).cloned().unwrap_or_else(|| {
                    state
                        .as_ref()
                        .filter(|s| s.observed_at.is_some())
                        .map(|s| {
                            format!(
                                "{}{}",
                                if s.stale { "Stale\n" } else { "" },
                                driver.display(
                                    &s.last_value,
                                    binding.action.rsplit('.').next().unwrap_or(""),
                                    &self.settings(id)
                                )
                            )
                        })
                        .unwrap_or_else(|| "Waiting for connection".into())
                });
                let visual = state
                    .as_ref()
                    .map(|s| {
                        driver.visual_state(
                            &s.last_value,
                            binding.action.rsplit('.').next().unwrap_or(""),
                        )
                    })
                    .unwrap_or(0);
                self.plugin.renderer.render(
                    client,
                    id,
                    Display {
                        title: text,
                        state: visual,
                        ..Display::default()
                    },
                )?;
            }
        }
        Ok(())
    }
    // Start the requested observation or operation for connector observations
    fn start(&mut self, client: &Client) -> Result<()> {
        if self.active.is_some() {
            return Ok(());
        }
        self.watch()?;
        if self.queue.is_empty() {
            if let Some(key) = self.resources.due((self.plugin.clock)()).first().cloned() {
                let settings = if let Ok(driver) = self.driver.try_lock() {
                    self.plugin
                        .bindings
                        .keys()
                        .map(|id| (id.clone(), self.settings(id)))
                        .filter(|(_, s)| driver.key(s) == key)
                        .min_by_key(|(_, s)| {
                            let interval =
                                crate::polling::update_interval(s, self.options.interval_ms);
                            if interval == 0 {
                                u64::MAX
                            } else {
                                interval
                            }
                        })
                } else {
                    None
                };
                if let Some((context, settings)) = settings {
                    let generation = self.resources.begin(&key)?;
                    self.queue.push_back(Request {
                        context,
                        settings,
                        resource: Some((key, generation)),
                        request_id: None,
                        scope: String::new(),
                        action: String::new(),
                        op: String::new(),
                        ticks: 0,
                        receipt: None,
                    });
                }
            }
        }
        let Some(request) = self.queue.pop_front() else {
            return Ok(());
        };
        self.sequence += 1;
        let generation = self.sequence;
        let cancel = Arc::new(AtomicBool::new(false));
        let flag = cancel.clone();
        let keep_connection = Arc::new(AtomicBool::new(false));
        let keep = keep_connection.clone();
        let driver = self.driver.clone();
        let sender = self.sender.clone();
        let client = client.clone();
        let settings = request.settings.clone();
        let action = request.action.clone();
        let op = request.op.clone();
        let ticks = request.ticks;
        let read = request.resource.is_some();
        let lease = client.pin_redactor();
        let work = move || {
            let _lease = lease;
            let mut driver = driver.lock().unwrap();
            let result = (|| -> Result<Value> {
                if flag.load(Ordering::Acquire) {
                    return Err(error("cancelled"));
                }
                if read || op == "test" {
                    let value = driver.read(flag.clone(), &settings)?;
                    return Ok(if read {
                        value
                    } else {
                        json!({"message":"Connection succeeded"})
                    });
                }
                if op == "discover" {
                    return Ok(
                        json!({"devices":driver.discover(flag.clone(), &settings)?,"message":"Request completed"}),
                    );
                }
                serde_json::to_value(driver.command(
                    flag.clone(),
                    if op.is_empty() { &action } else { &op },
                    &settings,
                    ticks,
                )?)
                .map_err(Into::into)
            })();
            let (value, failure, retry_at) = match result {
                Ok(value) => (value, None, None),
                Err(failure) => {
                    let retry = failure
                        .downcast_ref::<crate::service::HTTPError>()
                        .and_then(|e| e.retry_at);
                    (
                        Value::Null,
                        Some(crate::credentials::safe_error(failure, &settings, &[]).to_string()),
                        retry,
                    )
                }
            };
            if flag.load(Ordering::Acquire) && !keep.load(Ordering::Acquire) {
                driver.close();
            }
            let _ = sender.try_send(Completed {
                generation,
                value,
                failure,
                retry_at,
            });
            client.wake();
        };
        if !self.pool.submit(cancel.clone(), work) {
            return Err(error("worker queue full"));
        }
        let deadline = std::time::Instant::now()
            + std::time::Duration::from_millis(if request.resource.is_some() {
                self.options.read_timeout_ms
            } else if request.op == "authorize" {
                self.options.authorize_timeout_ms
            } else {
                self.options.command_timeout_ms
            });
        self.active = Some(Active {
            keep_connection,
            deadline,
            request,
            generation,
            cancel,
        });
        Ok(())
    }
    // Collect completed output from the owner for connector observations
    fn drain(&mut self, client: &Client) -> Result<()> {
        while let Ok(result) = self.receiver.try_recv() {
            if !self
                .active
                .as_ref()
                .is_some_and(|a| a.generation == result.generation)
            {
                continue;
            }
            let active = self.active.take().unwrap();
            let request = active.request;
            if let Some((key, generation)) = request.resource {
                let previous = self.resources.states.get(&key).cloned().unwrap_or_default();
                if self.resources.complete(
                    &key,
                    generation,
                    PollResult {
                        value: result.value,
                        failed: result.failure.is_some(),
                        now: (self.plugin.clock)(),
                        jitter: 0.5,
                        retry_at: result.retry_at,
                    },
                ) {
                    if client.supports("plugin-logs-v1") {
                        if let Some(failure) = &result.failure {
                            let failures = previous.failures + 1;
                            if failures.is_power_of_two() {
                                let _=client.log_stream("stderr",&format!("Connection read failed ({failures} consecutive failures): {failure}"));
                            }
                        } else if previous.observed_at.is_none() || previous.failures > 0 {
                            let _ = client.log_stream("stdout", "Connection available");
                        }
                    }
                    self.messages.clear();
                }
            } else {
                let message = result
                    .failure
                    .as_ref()
                    .map(|e| format!("Unavailable\n{e}"))
                    .unwrap_or_else(|| {
                        result.value["message"]
                            .as_str()
                            .unwrap_or("Request completed")
                            .into()
                    });
                self.messages
                    .insert(request.context.clone(), message.clone());
                if let Some(receipt) = request.receipt {
                    receipt.finish(if let Some(failure) = &result.failure {
                        Err(error(failure.clone()))
                    } else {
                        Ok(())
                    });
                }
                if !request.op.is_empty() {
                    let mut payload = json!({"message":message,"devices":result.value["devices"]});
                    if let Some(id) = request.request_id {
                        payload = crate::discovery::envelope(
                            &crate::discovery::Request {
                                context: request.context.clone(),
                                action: request.action.clone(),
                                channel: "service".into(),
                                id,
                                operation: request.op.clone(),
                                scope: request.scope.clone(),
                            },
                            payload.as_object().unwrap().clone(),
                            if result.failure.is_none() {
                                ""
                            } else {
                                "failed"
                            },
                        )?
                        .payload;
                    }
                    client.to_inspector(&request.context, &request.action, payload)?;
                }
                for state in self.resources.states.values_mut() {
                    state.next = 0;
                }
            }
        }
        Ok(())
    }
}
impl Handler for Connector {
    // Dispatch the received event to the registered behavior for connector observations
    fn handle(&mut self, client: &Client, event: &Event) -> Result<()> {
        self.drain(client)?;
        let shared = &self.options.shared_keys;
        let global_change = event.event == "didReceiveGlobalSettings"
            && (shared.is_empty()
                || shared
                    .iter()
                    .any(|k| self.plugin.globals.get(k) != event.settings().get(k)));
        if global_change || (event.event == "didReceiveSettings" && shared.is_empty()) {
            self.reset();
        } else if matches!(event.event.as_str(), "didReceiveSettings" | "willDisappear") {
            self.cancel_binding(&event.context);
            if event.event == "willDisappear" {
                self.resources.unwatch(&event.context);
            }
        }
        self.plugin.handle(client, event)?;
        if event.event == "willDisappear" {
            self.resources.unwatch(&event.context);
            self.messages.remove(&event.context);
            if self.plugin.bindings.is_empty() {
                self.reset();
            }
        }
        if event.event == "propertyInspectorDidDisappear"
            && !self.plugin.bindings.contains_key(&event.context)
        {
            self.cancel_binding(&event.context);
        }
        let op = event.payload["op"].as_str().unwrap_or("");
        let inspect = event.event == "sendToPlugin"
            && ["test", "discover", "authorize", "disconnect"].contains(&op);
        let input = matches!(event.event.as_str(), "keyDown" | "dialDown" | "touchTap")
            || (event.event == "dialRotate" && self.batch_dial(&event.action));
        if inspect || input {
            let request_id = if inspect {
                match event.payload.get("request_id") {
                    None => None,
                    Some(Value::String(id)) if id.len() <= 128 => Some(id.clone()),
                    _ => return Err(error("invalid discovery request ID")),
                }
            } else {
                None
            };
            if self.queue.len() >= 32 {
                if let Some(id) = request_id {
                    client.to_inspector(&event.context,&event.action,json!({"request_id":id,"available":false,"error":"Command queue full; retry after it drains"}))?;
                }
                return Err(error("command queue full"));
            }
            let settings = if inspect {
                crate::settings::project(
                    &self.plugin.globals,
                    &event.payload["settings"],
                    if event.payload["scope"] == "plugin" {
                        &[]
                    } else {
                        &self.options.shared_keys
                    },
                )
            } else {
                self.settings(&event.context)
            };
            self.queue.push_back(Request {
                context: event.context.clone(),
                request_id,
                scope: event.payload["scope"].as_str().unwrap_or("action").into(),
                action: if inspect {
                    event.action.clone()
                } else {
                    event.action.rsplit('.').next().unwrap_or("").into()
                },
                op: if inspect { op.into() } else { String::new() },
                settings,
                ticks: if event.event == "dialRotate" {
                    event.payload["ticks"].as_i64().unwrap_or(0) as i32
                } else {
                    0
                },
                receipt: Some(client.defer_action()),
                resource: None,
            });
        }
        self.start(client)?;
        self.render(client)
    }
    // Advance scheduled work on the owning event loop for connector observations
    fn poll(&mut self, client: &Client) -> Result<()> {
        if self
            .active
            .as_ref()
            .is_some_and(|active| std::time::Instant::now() >= active.deadline)
        {
            let mut active = self.active.take().unwrap();
            active.cancel.store(true, Ordering::Release);
            if let Some(receipt) = active.request.receipt.take() {
                receipt.finish(Err(error("connector deadline exceeded")));
            }
            if let Some((key, generation)) = active.request.resource.take() {
                self.resources.complete(
                    &key,
                    generation,
                    PollResult {
                        failed: true,
                        now: (self.plugin.clock)(),
                        jitter: 0.5,
                        ..Default::default()
                    },
                );
            }
        }
        self.drain(client)?;
        if let Ok(mut driver) = self.driver.try_lock() {
            if driver.changed() {
                for state in self.resources.states.values_mut() {
                    state.next = 0;
                }
            }
        }
        self.start(client)?;
        self.render(client)
    }
    // Return the next deadline that requires owner-loop attention
    fn next_poll(&self, now: std::time::Instant) -> Option<std::time::Instant> {
        if let Some(active) = &self.active {
            Some(active.deadline)
        } else if !self.queue.is_empty() {
            Some(now)
        } else {
            let clock = (self.plugin.clock)();
            self.resources
                .states
                .keys()
                .map(|key| self.resources.deadline(key))
                .filter(|next| *next != crate::polling::NEVER)
                .map(|next| now + std::time::Duration::from_millis(next.saturating_sub(clock)))
                .min()
        }
    }

    // Handle a compatible batch of dial input
    fn batch_dial(&self, action: &str) -> bool {
        self.options
            .dial_actions
            .iter()
            .any(|a| a == action.rsplit('.').next().unwrap_or(action))
    }
    // Close the owned session and release pending work for connector observations
    fn close(&mut self) -> Result<()> {
        self.reset();
        self.pool.close();
        self.plugin.close()
    }
}
