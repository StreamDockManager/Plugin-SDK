use crate::*;
use serde_json::{json, Value};
use std::{
    collections::{HashMap, VecDeque},
    net::{SocketAddr, TcpStream},
    sync::{
        atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering},
        mpsc::{sync_channel, Receiver, SyncSender, TryRecvError},
        Arc, Mutex,
    },
    thread,
    time::{Duration, Instant},
};
use tungstenite::{protocol::WebSocketConfig, Message};

/// Complete UTF-8 JSON envelope limit, including legacy hosts
pub const MAX_MESSAGE_BYTES: usize = 1 << 20;
pub const MAX_OUTPUT_BYTES: usize = 8 * MAX_MESSAGE_BYTES;
pub const MAX_LOG_BYTES: usize = MAX_MESSAGE_BYTES;
pub const MAX_INPUT_BYTES: usize = 8 * MAX_MESSAGE_BYTES;
pub const MAX_INPUT_ITEMS: usize = 128;

// Input capacity covers queued, selected and batched normal events. Control
// responses are correlated by the I/O pump before normal event admission
#[derive(Default)]
struct IncomingBudget(Arc<Mutex<(usize, usize)>>);
struct IncomingLease {
    budget: Arc<Mutex<(usize, usize)>>,
    bytes: usize,
}
impl IncomingBudget {
    // Reserve admission capacity for queued input
    fn acquire(&self, bytes: usize) -> Result<IncomingLease> {
        let mut owned = self.0.lock().unwrap();
        if bytes > MAX_MESSAGE_BYTES {
            return Err(error("SDK incoming message exceeds limit"));
        }
        if owned.0 >= MAX_INPUT_ITEMS || owned.1 + bytes > MAX_INPUT_BYTES {
            return Err(error("SDK incoming queue full"));
        }
        owned.0 += 1;
        owned.1 += bytes;
        Ok(IncomingLease {
            budget: self.0.clone(),
            bytes,
        })
    }
}
impl Drop for IncomingLease {
    // Release owned resources when the instance leaves scope
    fn drop(&mut self) {
        let mut owned = self.budget.lock().unwrap();
        owned.0 -= 1;
        owned.1 -= self.bytes;
    }
}
struct QueuedMessage {
    value: Value,
    bytes: usize,
}
/// A receiver that releases byte capacity as messages are consumed
pub struct OutboundReceiver {
    receiver: Receiver<QueuedMessage>,
    bytes: Arc<AtomicUsize>,
}
impl OutboundReceiver {
    // Return reserved capacity to the shared budget
    fn release(&self, message: QueuedMessage) -> Value {
        self.bytes.fetch_sub(message.bytes, Ordering::AcqRel);
        message.value
    }
    // Receive an available event without blocking
    pub fn try_recv(&self) -> std::result::Result<Value, TryRecvError> {
        self.receiver
            .try_recv()
            .map(|message| self.release(message))
    }
    // Wait for the next admitted event
    pub fn recv(&self) -> std::result::Result<Value, std::sync::mpsc::RecvError> {
        self.receiver.recv().map(|message| self.release(message))
    }
    // Wait for an event until the supplied timeout
    pub fn recv_timeout(
        &self,
        timeout: Duration,
    ) -> std::result::Result<Value, std::sync::mpsc::RecvTimeoutError> {
        self.receiver
            .recv_timeout(timeout)
            .map(|message| self.release(message))
    }
    // Iterate over currently available admitted events
    pub fn try_iter(&self) -> impl Iterator<Item = Value> + '_ {
        std::iter::from_fn(|| self.try_recv().ok())
    }
}

type Apply = Box<dyn FnOnce(&Client, Value) -> Result<()> + Send>;
struct JobResult {
    id: u64,
    value: Result<Value>,
    apply: Apply,
}
struct Job {
    context: String,
    cancel: Arc<AtomicBool>,
    complete: Deferred,
}
struct PendingInput {
    context: String,
    complete: Deferred,
}
type LogRedactor = Arc<dyn Fn(&str) -> String + Send + Sync>;
pub(crate) struct Inner {
    pub(crate) plugin_theme: Mutex<Option<crate::PluginTheme>>,
    logs: Mutex<VecDeque<QueuedMessage>>,
    dropped_logs: AtomicU64,
    separate_logs: AtomicBool,
    log_redactor: Mutex<Option<LogRedactor>>,
    host: HostInfo,
    identity: String,
    out: SyncSender<QueuedMessage>,
    out_bytes: Arc<AtomicUsize>,
    stop: Arc<AtomicBool>,
    wake: AtomicBool,
    notifier: Mutex<Option<Arc<mio::Waker>>>,
    owner_thread: Mutex<Option<std::thread::Thread>>,
    pub(crate) requests: Mutex<crate::hoststorage::Requests>,
    network_closed: AtomicBool,
    current: Mutex<Option<Completion>>,
    inputs: Mutex<HashMap<String, PendingInput>>,
    actions: Mutex<HashMap<u64, (String, Completion)>>,
    jobs: Mutex<HashMap<u64, Job>>,
    pool: Mutex<ProbePool>,
    results: SyncSender<JobResult>,
    result_rx: Mutex<Receiver<JobResult>>,
    sequence: AtomicU64,
}
impl Inner {
    // Wake the owner loop for newly available work
    fn notify(&self) {
        if let Some(owner) = self.owner_thread.lock().unwrap().as_ref() {
            owner.unpark();
        }
        if let Some(waker) = self.notifier.lock().unwrap().as_ref() {
            let _ = waker.wake();
        }
    }
    // Admit the message under queue and byte limits
    fn enqueue(&self, value: Value) -> Result<()> {
        let bytes = serde_json::to_vec(&value)?.len();
        if bytes > MAX_MESSAGE_BYTES {
            return Err(error("SDK message exceeds 1048576-byte UTF-8 JSON limit"));
        }
        let mut used = self.out_bytes.load(Ordering::Acquire);
        loop {
            if used + bytes > MAX_OUTPUT_BYTES {
                return Err(error("SDK outbound byte budget exceeded"));
            }
            match self.out_bytes.compare_exchange_weak(
                used,
                used + bytes,
                Ordering::AcqRel,
                Ordering::Acquire,
            ) {
                Ok(_) => break,
                Err(current) => used = current,
            }
        }
        if self.out.try_send(QueuedMessage { value, bytes }).is_err() {
            self.out_bytes.fetch_sub(bytes, Ordering::AcqRel);
            return Err(error("SDK outbound queue full"));
        }
        self.notify();
        Ok(())
    }
    // Queue a bounded diagnostic log frame
    fn queue_log(&self, message: Value) -> Result<()> {
        if self.stop.load(Ordering::Acquire) {
            return Err(error("SDK session closed"));
        }
        if serde_json::to_vec(&message)?.len() > MAX_MESSAGE_BYTES {
            return Err(error("SDK message exceeds 1048576-byte UTF-8 JSON limit"));
        }
        if !self.separate_logs.load(Ordering::Acquire) {
            return self.enqueue(message);
        }
        let mut logs = self.logs.lock().unwrap();
        if logs.len() >= 128
            || logs.iter().map(|value| value.bytes).sum::<usize>()
                + serde_json::to_vec(&message)?.len()
                > MAX_LOG_BYTES
        {
            self.dropped_logs.fetch_add(1, Ordering::Relaxed);
            return Err(error("SDK log queue full"));
        }
        logs.push_back(QueuedMessage {
            bytes: serde_json::to_vec(&message)?.len(),
            value: message,
        });
        self.notify();
        Ok(())
    }
}
#[derive(Clone)]
pub struct Client {
    pub(crate) inner: Arc<Inner>,
    pub network: NetworkPayload,
}
impl Client {
    /// Construct an offline client and bounded outbound receiver, also useful for tests
    pub fn new(host: HostInfo, identity: String) -> (Self, OutboundReceiver) {
        let (out, rx) = sync_channel(128);
        let out_bytes = Arc::new(AtomicUsize::new(0));
        let (results, result_rx) = sync_channel(36);
        (
            Self {
                inner: Arc::new(Inner {
                    plugin_theme: Mutex::new(None),
                    logs: Mutex::new(VecDeque::new()),
                    dropped_logs: AtomicU64::new(0),
                    separate_logs: AtomicBool::new(false),
                    log_redactor: Mutex::new(None),
                    host,
                    identity,
                    out,
                    out_bytes: out_bytes.clone(),
                    stop: Arc::new(AtomicBool::new(false)),
                    wake: AtomicBool::new(false),
                    notifier: Mutex::new(None),
                    owner_thread: Mutex::new(None),
                    requests: Mutex::new(Default::default()),
                    network_closed: AtomicBool::new(false),
                    current: Mutex::new(None),
                    inputs: Mutex::new(HashMap::new()),
                    actions: Mutex::new(HashMap::new()),
                    jobs: Mutex::new(HashMap::new()),
                    pool: Mutex::new(ProbePool::default()),
                    results,
                    result_rx: Mutex::new(result_rx),
                    sequence: AtomicU64::new(0),
                }),
                network: NetworkPayload::default(),
            },
            OutboundReceiver {
                receiver: rx,
                bytes: out_bytes,
            },
        )
    }
    /// Production and recorders isolate diagnostics from normal outbound capacity
    /// The original offline receiver remains compatible until this is enabled
    pub fn isolate_logs(&self) {
        self.inner.separate_logs.store(true, Ordering::Release);
    }
    // Drain queued diagnostic frames for transport
    pub fn drain_logs(&self) -> Vec<Value> {
        let mut values = Vec::new();
        let lost = self.inner.dropped_logs.swap(0, Ordering::AcqRel);
        if lost > 0 {
            let mut payload = json!({"message":format!("SDK log queue overloaded; {lost} messages were not captured"),"incomplete":true});
            if self.supports("plugin-logs-v1") {
                payload["stream"] = json!("stderr");
            }
            values.push(json!({"event":"logMessage","payload":payload}));
        }
        values.extend(
            self.inner
                .logs
                .lock()
                .unwrap()
                .drain(..)
                .map(|message| message.value),
        );
        values
    }
    // Enable or disable network diagnostic collection
    pub fn set_network_debug(&self, enabled: bool) {
        let output = Arc::downgrade(&self.inner);
        let redactor = output.clone();
        self.network.diagnostics.configure(
            enabled && self.supports("plugin-logs-v1"),
            // Forward diagnostics only while the runtime still exists, without extending its lifetime
            move |payload, failed| {
                if let Some(inner) = output.upgrade() {
                    let mut payload = payload;
                    payload["source"] = json!("network");
                    payload["stream"] = json!(if failed { "stderr" } else { "stdout" });
                    let _ = inner.queue_log(json!({"event":"logMessage","payload":payload}));
                }
            },
            // Resolve the current redactor without retaining the runtime through the diagnostics callback
            move |text| {
                let redact = redactor
                    .upgrade()
                    .and_then(|inner| inner.log_redactor.lock().unwrap().clone());
                redact.map(|f| f(text)).unwrap_or_else(|| text.into())
            },
        );
    }
    // Read the negotiated host descriptor
    pub fn host(&self) -> &HostInfo {
        &self.inner.host
    }
    // Check whether the negotiated capability is available
    pub fn supports(&self, feature: &str) -> bool {
        self.host().supports(feature)
    }
    // Request orderly session shutdown
    pub fn stop(&self) {
        self.cancel_requests();
        self.set_network_debug(false);
        self.inner.stop.store(true, Ordering::Release);
        self.inner.notify()
    }
    // Report whether cancellation has been requested
    pub fn cancelled(&self) -> Arc<AtomicBool> {
        self.inner.stop.clone()
    }
    // Wake the owning event loop
    pub fn wake(&self) {
        self.inner.wake.store(true, Ordering::Release);
        self.inner.notify()
    }
    // Queue the outbound message for the owned connection
    pub fn send(&self, event: &str, context: &str, payload: Value) -> Result<()> {
        self.send_with_action(event, context, None, payload)
    }
    // Admit a protocol control request to the output queue
    pub(crate) fn enqueue_control(&self, value: Value) -> Result<()> {
        self.ensure_request_open()?;
        self.inner.enqueue(value)
    }
    // Reject requests after the session has closed
    pub(crate) fn ensure_request_open(&self) -> Result<()> {
        if self.inner.stop.load(Ordering::Acquire)
            || self.inner.network_closed.load(Ordering::Acquire)
        {
            return Err(Box::new(crate::RpcError {
                code: "cancelled_before_commit".into(),
                message: "host session is closed; request was not enqueued".into(),
            }));
        }
        Ok(())
    }
    // Queue a message with the action identity required by the host
    fn send_with_action(
        &self,
        event: &str,
        context: &str,
        action: Option<&str>,
        payload: Value,
    ) -> Result<()> {
        if self.inner.stop.load(Ordering::Acquire)
            || self.inner.network_closed.load(Ordering::Acquire)
        {
            return Err(error("SDK session closed"));
        }
        if let Some(feature) = required_capability(event) {
            if !self.supports(feature) {
                return Err(Box::new(UnsupportedFeatureError(feature.into())));
            }
        }
        let mut message = json!({"event":event});
        if let Some(action) = action {
            message["action"] = json!(action)
        }
        if !context.is_empty() {
            message["context"] = json!(context)
        }
        if !payload.is_null() {
            message["payload"] = payload.clone()
        }
        if serde_json::to_vec(&message)?.len() > MAX_MESSAGE_BYTES {
            return Err(error("SDK message exceeds 1048576-byte UTF-8 JSON limit"));
        }
        if event == "logMessage" {
            return self.inner.queue_log(message);
        }
        let request = if event == "desktopInput" && self.inner.current.lock().unwrap().is_some() {
            let id = payload["request_id"]
                .as_str()
                .filter(|v| !v.is_empty())
                .ok_or_else(|| error("desktop input requires a request ID"))?
                .to_string();
            let mut inputs = self.inner.inputs.lock().unwrap();
            if inputs.len() >= 128 || inputs.contains_key(&id) {
                return Err(error("duplicate or excessive desktop input request ID"));
            }
            inputs.insert(
                id.clone(),
                PendingInput {
                    context: context.into(),
                    complete: self.defer_action(),
                },
            );
            Some(id)
        } else {
            None
        };
        if let Err(failure) = self.inner.enqueue(message) {
            if let Some(id) = request {
                self.inner.inputs.lock().unwrap().remove(&id);
            }
            return Err(failure);
        }
        Ok(())
    }
    // Queue a control title update
    pub fn title(&self, id: &str, text: &str) -> Result<()> {
        self.send("setTitle", id, json!({"title":text,"target":0}))
    }
    // Queue a control image update
    pub fn image(&self, id: &str, image: &str) -> Result<()> {
        self.send("setImage", id, json!({"image":image,"target":0}))
    }
    // Queue an action state update
    pub fn state(&self, id: &str, state: u32) -> Result<()> {
        self.send("setState", id, json!({"state":state}))
    }
    // Queue encoder feedback for the selected action
    pub fn feedback(&self, id: &str, value: Value) -> Result<()> {
        self.send("setFeedback", id, value)
    }
    // Queue the encoder feedback layout
    pub fn feedback_layout(&self, id: &str, layout: &str) -> Result<()> {
        self.send("setFeedbackLayout", id, json!({"layout":layout}))
    }
    // Apply or retrieve the action settings in this scope for plugin runtime dispatch
    pub fn settings(&self, id: &str, value: Value) -> Result<()> {
        self.send("setSettings", id, value)
    }
    // Request action settings from the host
    pub fn get_settings(&self, id: &str) -> Result<()> {
        self.send("getSettings", id, Value::Null)
    }
    // Queue a replacement of plugin-global settings
    pub fn global_settings(&self, value: Value) -> Result<()> {
        self.send("setGlobalSettings", &self.inner.identity, value)
    }
    // Request plugin-global settings from the host
    pub fn get_global_settings(&self) -> Result<()> {
        self.send("getGlobalSettings", &self.inner.identity, Value::Null)
    }
    // Send an action-scoped property-inspector message
    pub fn to_inspector(&self, id: &str, action: &str, value: Value) -> Result<()> {
        if action.is_empty() {
            return Err(error("inspector messaging requires an action UUID"));
        }
        self.send_with_action("sendToPropertyInspector", id, Some(action), value)
    }
    // Request the host alert indicator
    pub fn alert(&self, id: &str) -> Result<()> {
        self.send("showAlert", id, json!({}))
    }
    // Request the host success indicator
    pub fn ok(&self, id: &str) -> Result<()> {
        self.send("showOk", id, json!({}))
    }
    // Queue a plugin diagnostic message
    pub fn log(&self, message: &str) -> Result<()> {
        self.log_stream("stdout", message)
    }
    // Install the redactor used before logs are framed
    pub fn set_log_redactor(&self, redact: impl Fn(&str) -> String + Send + Sync + 'static) {
        *self.inner.log_redactor.lock().unwrap() = Some(Arc::new(redact));
    }
    // Apply the configured redactor to diagnostic text
    fn redact_log(&self, text: &str) -> String {
        let redact = self.inner.log_redactor.lock().unwrap().clone();
        redact.map(|f| f(text)).unwrap_or_else(|| text.into())
    }
    // Create a writer that frames text as plugin logs
    pub fn log_stream(&self, stream: &str, text: &str) -> Result<()> {
        if !["stdout", "stderr"].contains(&stream) {
            return Err(error("invalid log stream"));
        }
        let text = self.redact_log(text);
        let mut remaining = text.as_str();
        while !remaining.is_empty() {
            let mut end = remaining.len().min(16 << 10);
            while !remaining.is_char_boundary(end) {
                end -= 1;
            }
            let mut payload = json!({"message": &remaining[..end]});
            if self.supports("plugin-logs-v1") {
                payload["stream"] = json!(stream);
            }
            self.send("logMessage", "", payload)?;
            remaining = &remaining[end..];
        }
        Ok(())
    }
    // Create the standard-output diagnostic writer
    pub fn stdout(&self) -> LogWriter {
        LogWriter {
            client: self.clone(),
            stream: "stdout",
        }
    }
    // Create the standard-error diagnostic writer
    pub fn stderr(&self) -> LogWriter {
        LogWriter {
            client: self.clone(),
            stream: "stderr",
        }
    }
    // Request the host to open a permitted URL
    pub fn open_url(&self, url: &str) -> Result<()> {
        self.send("openUrl", "", json!({"url":url}))
    }
    // Publish dynamic inspector choices
    pub fn dynamic_items(&self, value: Value) -> Result<()> {
        self.send("setDynamicItems", "", value)
    }
    // Request telemetry through the negotiated capability
    pub fn subscribe_telemetry(&self, topics: &[&str]) -> Result<()> {
        self.send("subscribeTelemetry", "", json!({"topics":topics}))
    }
    // Submit desktop input with its receipt and cancellation scope
    pub fn desktop_input(&self, id: &str, value: Value) -> Result<()> {
        self.send("desktopInput", id, value)
    }
    // Produce the current control display
    pub fn display(&self, id: &str, value: &Display) -> Result<()> {
        self.state(id, value.state)?;
        self.title(id, &value.title)?;
        if !value.image.is_empty() {
            self.image(id, &value.image)?
        }
        {
            if !value.layout.is_empty() {
                self.feedback_layout(id, &value.layout)?
            }
            if !value.feedback.is_null() {
                self.feedback(id, value.feedback.clone())?
            }
        }
        Ok(())
    }
    // Register a deferred action completion
    pub fn defer_action(&self) -> Deferred {
        self.inner
            .current
            .lock()
            .unwrap()
            .as_ref()
            .map(Completion::defer)
            .unwrap_or(Deferred { action: None })
    }
    // Submit cancellable work outside the owner event loop
    pub fn background(
        &self,
        context: &str,
        work: impl FnOnce(Arc<AtomicBool>) -> Result<Value> + Send + 'static,
        apply: impl FnOnce(&Client, Value) -> Result<()> + Send + 'static,
    ) -> Result<Arc<AtomicBool>> {
        let mut jobs = self.inner.jobs.lock().unwrap();
        if jobs.len() >= 36 {
            return Err(error("SDK background queue full"));
        }
        let id = self.inner.sequence.fetch_add(1, Ordering::Relaxed);
        let cancel = Arc::new(AtomicBool::new(false));
        jobs.insert(
            id,
            Job {
                context: context.into(),
                cancel: cancel.clone(),
                complete: self.defer_action(),
            },
        );
        drop(jobs);
        let sender = self.inner.results.clone();
        let weak = Arc::downgrade(&self.inner);
        let cancelled = cancel.clone();
        if !self
            .inner
            .pool
            .lock()
            .unwrap()
            // Catch worker panics and return results only to the still-live action lifecycle
            .submit(cancel.clone(), move || {
                let value = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                    work(cancelled.clone())
                }))
                .unwrap_or_else(|_| Err(error("background job panicked")));
                if !cancelled.load(Ordering::Acquire) {
                    let _ = sender.send(JobResult {
                        id,
                        value,
                        apply: Box::new(apply),
                    });
                    if let Some(inner) = weak.upgrade() {
                        inner.wake.store(true, Ordering::Release);
                        inner.notify()
                    }
                }
            })
        {
            self.inner.jobs.lock().unwrap().remove(&id);
            return Err(error("SDK background queue full"));
        }
        Ok(cancel)
    }
    // Collect finished background work for the owner loop
    pub fn drain_background(&self) {
        self.drain_results()
    }
    // Apply completed results to their live action contexts
    fn drain_results(&self) {
        loop {
            let result = self.inner.result_rx.lock().unwrap().try_recv();
            let Ok(result) = result else { break };
            let job = self.inner.jobs.lock().unwrap().remove(&result.id);
            if let Some(job) = job {
                let value = if job.cancel.load(Ordering::Acquire) {
                    Err(error("background job cancelled"))
                } else {
                    result.value.and_then(|v| (result.apply)(self, v))
                };
                job.complete.finish(value);
            }
        }
    }
    // Cancel work whose action context is no longer live
    fn cancel_context(&self, context: Option<&str>) {
        let inputs = {
            let mut all = self.inner.inputs.lock().unwrap();
            let ids: Vec<_> = all
                .iter()
                .filter(|(_, v)| context.is_none_or(|c| v.context == c))
                .map(|(k, _)| k.clone())
                .collect();
            ids.into_iter()
                .filter_map(|id| all.remove(&id))
                .collect::<Vec<_>>()
        };
        drop(inputs);
        let jobs = {
            let mut all = self.inner.jobs.lock().unwrap();
            let ids: Vec<_> = all
                .iter()
                .filter(|(_, v)| context.is_none_or(|c| v.context == c))
                .map(|(k, _)| *k)
                .collect();
            ids.into_iter()
                .filter_map(|id| all.remove(&id))
                .collect::<Vec<_>>()
        };
        for job in jobs {
            job.cancel.store(true, Ordering::Release);
            drop(job)
        }
        let actions = {
            let mut all = self.inner.actions.lock().unwrap();
            let ids: Vec<_> = all
                .iter()
                .filter(|(_, v)| context.is_none_or(|c| v.0 == c))
                .map(|(k, _)| *k)
                .collect();
            ids.into_iter()
                .filter_map(|id| all.remove(&id))
                .collect::<Vec<_>>()
        };
        for (_, action) in actions {
            action.cancel()
        }
    }
    // Deliver the event through the handler lifecycle
    pub fn dispatch(&self, handler: &mut impl Handler, event: &Event) -> Result<()> {
        if event.event == crate::THEME_EVENT && !self.observe_plugin_theme(&event.payload) {
            return Ok(());
        }
        if event.event == "didReceiveLogSettings" && self.supports("plugin-logs-v1") {
            if let Some(enabled) = event.payload["debug_network"].as_bool() {
                self.set_network_debug(enabled);
            }
            return Ok(());
        }
        let log_action = self.supports("plugin-logs-v1") && handler.should_log_action(event);
        let started = Instant::now();
        if event.event == "willDisappear" {
            self.cancel_context(Some(&event.context))
        }
        let request = event
            .surface
            .as_ref()
            .filter(|s| !s.request_id.is_empty() && self.supports("action-results-v1"))
            .map(|s| s.request_id.clone());
        let action = if request.is_some() || log_action {
            let context = event.context.clone();
            let name = event.action.clone();
            let key = self.inner.sequence.fetch_add(1, Ordering::Relaxed);
            let weak = Arc::downgrade(&self.inner);
            // Remove the receipt and report its outcome without keeping the runtime alive
            let completion = Completion::new(move |success| {
                if let Some(inner) = weak.upgrade() {
                    inner.actions.lock().unwrap().remove(&key);
                    if !inner.stop.load(Ordering::Acquire) {
                        if let Some(request) = &request {
                            let _ = inner.enqueue(json!({"event":"actionResult","context":context,"payload":{"request_id":request,"success":success}}));
                        }
                        if log_action {
                            let _ = inner.queue_log(json!({"event":"logMessage","payload":{"stream":if success {"stdout"}else{"stderr"},"message":format!("Action {name} {} in {} ms",if success {"completed"}else{"failed or cancelled"},started.elapsed().as_millis())}}));
                        }
                    }
                }
            });
            self.inner
                .actions
                .lock()
                .unwrap()
                .insert(key, (event.context.clone(), completion.clone()));
            Some(completion)
        } else {
            None
        };
        *self.inner.current.lock().unwrap() = action.clone();
        let outcome = handler.handle(self, event);
        if outcome.is_ok()
            && self.supports("plugin-logs-v1")
            && ["didReceiveSettings", "didReceiveGlobalSettings"].contains(&event.event.as_str())
        {
            let _ = self.log_stream("stdout", "Settings updated");
        }
        self.inner.current.lock().unwrap().take();
        if event.event == "desktopInputResult" {
            if let Some(request) = event.payload["request_id"].as_str() {
                let pending = {
                    let mut inputs = self.inner.inputs.lock().unwrap();
                    if inputs
                        .get(request)
                        .is_some_and(|v| v.context == event.context)
                    {
                        inputs.remove(request)
                    } else {
                        None
                    }
                };
                if let Some(input) = pending {
                    input
                        .complete
                        .finish(if event.payload["error"] == true || outcome.is_err() {
                            Err(error("desktop input failed"))
                        } else {
                            Ok(())
                        })
                }
            }
        }
        if let Err(failure) = &outcome {
            let _ = self.log_stream("stderr", &format!("Action failure: {failure}"));
            self.title(&event.context, &format!("Error: {failure}"))?;
            self.alert(&event.context)?
        }
        if let Some(action) = action {
            action.finish(outcome, true)
        }
        Ok(())
    }
    // Close the owned session and release pending work for plugin runtime dispatch
    fn close(&self) {
        self.set_network_debug(false);
        self.cancel_context(None);
        self.stop();
        self.inner.pool.lock().unwrap().close();
    }
}
#[derive(Default)]
pub struct Display {
    pub title: String,
    pub image: String,
    pub state: u32,
    pub layout: String,
    pub feedback: Value,
}
pub trait Handler {
    // Dispatch the received event to the registered behavior for plugin runtime dispatch
    fn handle(&mut self, client: &Client, event: &Event) -> Result<()>;
    // Advance scheduled work on the owning event loop for plugin runtime dispatch
    fn poll(&mut self, _client: &Client) -> Result<()> {
        Ok(())
    }
    // Return the next deadline that requires owner-loop attention
    fn next_poll(&self, _now: Instant) -> Option<Instant> {
        // Event-only handlers do not receive implicit periodic work
        None
    }
    // Decide whether this action event belongs in diagnostic logs
    fn should_log_action(&self, event: &Event) -> bool {
        should_log_action(event)
    }
    // Handle a compatible batch of dial input
    fn batch_dial(&self, _action: &str) -> bool {
        false
    }
    // Close the owned session and release pending work for plugin runtime dispatch
    fn close(&mut self) -> Result<()> {
        Ok(())
    }
}
// Decide whether this action event belongs in diagnostic logs
pub fn should_log_action(event: &Event) -> bool {
    match event.event.as_str() {
        "keyDown" | "dialDown" => true,
        "dialRotate" => event.payload["ticks"].as_i64().unwrap_or(0) != 0,
        "touchTap" => event
            .surface
            .as_ref()
            .and_then(|s| s.touch.as_ref())
            .map(|touch| ["tap", "end", ""].contains(&touch["phase"].as_str().unwrap_or("")))
            .unwrap_or_else(|| !event.payload["hold"].as_bool().unwrap_or(false)),
        _ => false,
    }
}
pub struct LaunchOptions {
    pub port: u16,
    pub identity: String,
    pub host: HostInfo,
}
impl LaunchOptions {
    // Decode the incoming representation into the typed contract for plugin runtime dispatch
    pub fn parse(args: impl IntoIterator<Item = String>) -> Result<Self> {
        let mut options = HashMap::new();
        let mut iter = args.into_iter();
        while let Some(key) = iter.next() {
            let key = key.trim_start_matches('-').to_string();
            if !["port", "pluginUUID", "registerEvent", "info"].contains(&key.as_str())
                || options.contains_key(&key)
            {
                return Err(error("invalid authenticated host arguments"));
            }
            options.insert(
                key,
                iter.next()
                    .ok_or_else(|| error("missing host argument value"))?,
            );
        }
        let port = options
            .get("port")
            .ok_or_else(|| error("missing host port"))?
            .parse()?;
        let identity = options
            .remove("pluginUUID")
            .filter(|v| !v.is_empty())
            .ok_or_else(|| error("missing host authentication"))?;
        if port == 0 || options.get("registerEvent").map(String::as_str) != Some("registerPlugin") {
            return Err(error("invalid authenticated host arguments"));
        }
        let host = HostInfo::parse(options.get("info").map(String::as_str).unwrap_or("{}"))?;
        Ok(Self {
            port,
            identity,
            host,
        })
    }
}
// Run the configured operation until completion or shutdown
pub fn run(
    handler: &mut impl Handler,
    args: impl IntoIterator<Item = String>,
    descriptor: Option<Capabilities>,
) -> Result<()> {
    let options = LaunchOptions::parse(args)?;
    connect(
        handler,
        options,
        descriptor,
        Arc::new(AtomicBool::new(false)),
    )
}
// On Windows the registered mio source must observe WouldBlock to re-arm its
// readiness. The WebSocket owns a cloned std handle, so route every operation
// through the source rather than reading the clone outside mio's bookkeeping
fn ready_io<T>(
    source: &mio::net::TcpStream,
    operation: impl FnOnce() -> tungstenite::Result<T>,
) -> tungstenite::Result<T> {
    source
        .try_io(|| match operation() {
            Err(tungstenite::Error::Io(error)) => Err(error),
            result => Ok(result),
        })
        .map_err(tungstenite::Error::Io)?
}

// Establish the connection using the configured trust policy
pub fn connect(
    handler: &mut impl Handler,
    options: LaunchOptions,
    descriptor: Option<Capabilities>,
    stop: Arc<AtomicBool>,
) -> Result<()> {
    if let Some(cap) = &descriptor {
        for required in &cap.required_capabilities {
            if !options.host.supports(required) {
                return Err(Box::new(UnsupportedFeatureError(required.clone())));
            }
        }
    }
    let (client, output) = Client::new(options.host, options.identity.clone());
    client.isolate_logs();
    client.set_network_debug(
        client.host().raw["streamDockManager"]["debug_network"]
            .as_bool()
            .unwrap_or(false),
    );
    *client.inner.owner_thread.lock().unwrap() = Some(thread::current());
    let result = (|| {
        let address = SocketAddr::from(([127, 0, 0, 1], options.port));
        let stream = TcpStream::connect_timeout(&address, Duration::from_secs(5))?;
        stream.set_read_timeout(Some(Duration::from_secs(5)))?;
        stream.set_write_timeout(Some(Duration::from_secs(5)))?;
        let config = WebSocketConfig::default()
            .max_message_size(Some(MAX_MESSAGE_BYTES))
            .max_frame_size(Some(MAX_MESSAGE_BYTES));
        let (mut socket, _) = tungstenite::client::client_with_config(
            format!("ws://127.0.0.1:{}", options.port),
            stream,
            Some(config),
        )
        .map_err(|_| error("host WebSocket handshake failed"))?;
        let mut registration = json!({"event":"registerPlugin","uuid":options.identity});
        if client.supports("plugin-protocol-v1") {
            registration["payload"] = json!({"capabilities":descriptor,"network_debug":true});
        }
        socket.send(Message::Text(registration.to_string().into()))?;
        socket.get_mut().set_read_timeout(None)?;
        socket.get_mut().set_nonblocking(true)?;
        let readiness = mio::Poll::new()?;
        let mut observed = mio::net::TcpStream::from_std(socket.get_ref().try_clone()?);
        readiness
            .registry()
            .register(&mut observed, mio::Token(0), mio::Interest::READABLE)?;
        *client.inner.notifier.lock().unwrap() = Some(Arc::new(mio::Waker::new(
            readiness.registry(),
            mio::Token(1),
        )?));
        let (input_tx, input_rx) = sync_channel(MAX_INPUT_ITEMS);
        let io_stop = Arc::new(AtomicBool::new(false));
        let pump_stop = io_stop.clone();
        let pump_client = client.clone();
        let owner = thread::current();
        let pump = thread::Builder::new()
            .name("stream-dock-manager-plugin-io".into())
            // Pump socket I/O separately while the owner thread serializes user callbacks
            .spawn(move || {
                let result = pump_transport(
                    socket,
                    observed,
                    readiness,
                    output,
                    input_tx,
                    &pump_client,
                    &pump_stop,
                );
                pump_client
                    .inner
                    .network_closed
                    .store(true, Ordering::Release);
                pump_client.cancel_requests();
                owner.unpark();
                result
            })?;
        // Only this thread dispatches callbacks, dial batches and background results
        // The I/O thread can continue receiving a requested storage response while a
        // callback waits synchronously for that response
        if client.supports_theme_rpc() {
            let _ = client.get_plugin_theme();
        }
        let handled = dispatch_transport(handler, &client, input_rx, &stop);
        client.cancel_context(None);
        io_stop.store(true, Ordering::Release);
        client.inner.notify();
        let pumped = pump.join().map_err(|_| error("host I/O worker panicked"))?;
        handled.and(pumped)
    })();
    client.close();
    *client.inner.owner_thread.lock().unwrap() = None;
    let closed = handler.close();
    result.and(closed)
}

// Move bounded messages between the socket and runtime queues
fn pump_transport(
    mut socket: tungstenite::WebSocket<TcpStream>,
    observed: mio::net::TcpStream,
    mut readiness: mio::Poll,
    output: OutboundReceiver,
    input: SyncSender<(Event, IncomingLease)>,
    client: &Client,
    stop: &AtomicBool,
) -> Result<()> {
    let incoming = IncomingBudget::default();
    let mut staged: Option<(Event, usize)> = None;
    let mut admitted: Option<(Event, IncomingLease)> = None;
    let mut events = mio::Events::with_capacity(8);
    let writes = |socket: &mut tungstenite::WebSocket<TcpStream>| -> Result<()> {
        for value in output.try_iter().take(128).chain(client.drain_logs()) {
            socket.get_mut().set_nonblocking(false)?;
            let written = ready_io(&observed, || {
                socket.send(Message::Text(value.to_string().into()))
            });
            socket.get_mut().set_nonblocking(true)?;
            written?;
        }
        Ok(())
    };
    loop {
        writes(&mut socket)?;
        if stop.load(Ordering::Acquire) {
            break;
        }
        if let Some((event, bytes)) = staged.take() {
            match incoming.acquire(bytes) {
                Ok(lease) => admitted = Some((event, lease)),
                Err(_) => staged = Some((event, bytes)),
            }
        }
        if let Some(event) = admitted.take() {
            match input.try_send(event) {
                Ok(()) => {
                    if let Some(owner) = client.inner.owner_thread.lock().unwrap().as_ref() {
                        owner.unpark();
                    }
                }
                Err(std::sync::mpsc::TrySendError::Full(event)) => admitted = Some(event),
                Err(std::sync::mpsc::TrySendError::Disconnected(_)) => break,
            }
        }
        if staged.is_some() || admitted.is_some() {
            // Retain at most one receive-stage envelope while applying normal
            // backpressure. The selected handler and dial batch remain charged
            readiness.poll(&mut events, Some(Duration::from_millis(10)))?;
            continue;
        }
        let message = match ready_io(&observed, || socket.read()) {
            Ok(message) => message,
            Err(tungstenite::Error::Io(failure))
                if matches!(
                    failure.kind(),
                    std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut
                ) =>
            {
                readiness.poll(&mut events, Some(Duration::from_millis(250)))?;
                continue;
            }
            Err(tungstenite::Error::ConnectionClosed | tungstenite::Error::AlreadyClosed) => break,
            Err(failure) => return Err(failure.into()),
        };
        if message.is_close() {
            break;
        }
        if !message.is_text() {
            continue;
        }
        if message.len() > MAX_MESSAGE_BYTES {
            return Err(error("SDK incoming message exceeds limit"));
        }
        let event = Event::parse(message.to_text()?)?;
        if client.observe_control_bytes(&event, message.len()) {
            continue;
        }
        staged = Some((event, message.len()));
    }
    writes(&mut socket)?;
    let _ = ready_io(&observed, || socket.close(None));
    Ok(())
}

// Dispatch admitted transport events on the handler thread
fn dispatch_transport(
    handler: &mut impl Handler,
    client: &Client,
    input: Receiver<(Event, IncomingLease)>,
    stop: &AtomicBool,
) -> Result<()> {
    let mut deadline = handler.next_poll(Instant::now());
    let mut backoff = 0;
    let mut backoff_until = Instant::now();
    let mut network = Instant::now() + Duration::from_secs(1);
    let mut batch: Option<(Event, Instant, IncomingLease)> = None;
    let flush = |batch: &mut Option<(Event, Instant, IncomingLease)>,
                 handler: &mut _,
                 client: &Client|
     -> Result<()> {
        if let Some((event, _, _lease)) = batch.take() {
            client.dispatch(handler, &event)?;
        }
        Ok(())
    };
    while !stop.load(Ordering::Acquire) && !client.inner.stop.load(Ordering::Acquire) {
        client.drain_results();
        let now = Instant::now();
        if batch.as_ref().is_some_and(|(_, due, _)| now >= *due) {
            flush(&mut batch, handler, client)?;
        }
        if client.inner.wake.swap(false, Ordering::AcqRel)
            || deadline.is_some_and(|d| now >= d.max(backoff_until))
        {
            match handler.poll(client) {
                Ok(()) => backoff = 0,
                Err(failure) => {
                    backoff = if backoff == 0 {
                        1
                    } else {
                        (backoff * 2).min(60)
                    };
                    let _ = client.log_stream("stderr", &failure.to_string());
                }
            }
            backoff_until = now + Duration::from_secs(backoff);
            deadline = handler.next_poll(Instant::now());
        }
        if now >= network {
            network = now + Duration::from_secs(1);
            if client.supports("network-payload-v1") {
                if let Some(run) = client.host().run_id() {
                    let (sent, received) = client.network.snapshot();
                    client.send(
                        "networkPayload",
                        "",
                        json!({"run_id":run,"sent_bytes":sent,"received_bytes":received}),
                    )?;
                }
            }
        }
        let (event, lease) = match input.try_recv() {
            Ok(event) => event,
            Err(TryRecvError::Disconnected) => break,
            Err(TryRecvError::Empty) => {
                let current = Instant::now();
                let mut wait = Duration::from_millis(250);
                for due in [
                    deadline.map(|d| d.max(backoff_until)),
                    batch.as_ref().map(|(_, d, _)| *d),
                    client
                        .host()
                        .run_id()
                        .filter(|_| client.supports("network-payload-v1"))
                        .map(|_| network),
                ]
                .into_iter()
                .flatten()
                {
                    wait = wait.min(due.saturating_duration_since(current));
                }
                thread::park_timeout(wait);
                continue;
            }
        };
        if event.event == "shutdown" {
            flush(&mut batch, handler, client)?;
            break;
        }
        if event.event == "dialRotate"
            && handler.batch_dial(&event.action)
            && event
                .surface
                .as_ref()
                .is_none_or(|s| s.request_id.is_empty())
        {
            let ticks = event.payload["ticks"].as_i64().unwrap_or(0);
            let mut merged = false;
            if let Some((old, due, _)) = &mut batch {
                let prior = old.payload["ticks"].as_i64().unwrap_or(0);
                if now < *due
                    && old.context == event.context
                    && old.action == event.action
                    && old.device == event.device
                    && (prior > 0) == (ticks > 0)
                {
                    if let Some(sum) = prior.checked_add(ticks) {
                        old.payload["ticks"] = json!(sum);
                        merged = true;
                    }
                }
            }
            if !merged {
                flush(&mut batch, handler, client)?;
                batch = Some((event, Instant::now() + Duration::from_millis(16), lease));
            }
        } else {
            flush(&mut batch, handler, client)?;
            client.dispatch(handler, &event)?;
        }
        // Notify the pump that selected-event byte capacity may now be released
        if let Some(waker) = client.inner.notifier.lock().unwrap().as_ref() {
            let _ = waker.wake();
        }
    }
    Ok(())
}

pub struct LogWriter {
    client: Client,
    stream: &'static str,
}
impl std::io::Write for LogWriter {
    // Write data through the owned transport or state backend for plugin runtime dispatch
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        self.client
            .log_stream(self.stream, &String::from_utf8_lossy(bytes))
            .map_err(|e| std::io::Error::other(e.to_string()))?;
        Ok(bytes.len())
    }
    // Write pending state or bytes to the owned destination
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::mpsc::channel;

    #[test]
    // Verify incoming admission includes selected and batched envelopes
    fn incoming_admission_includes_selected_and_batched_envelopes() {
        let fixture: Value =
            serde_json::from_str(include_str!("../testdata/protocol-v1.json")).unwrap();
        let limits = &fixture["incoming_admission"];
        assert_eq!(limits["bytes"], MAX_INPUT_BYTES);
        assert_eq!(limits["items"], MAX_INPUT_ITEMS);
        assert_eq!(limits["receive_staging_bytes"], MAX_MESSAGE_BYTES);
        let budget = IncomingBudget::default();
        let mut retained = Vec::new();
        for _ in 0..MAX_INPUT_BYTES / MAX_MESSAGE_BYTES {
            retained.push(budget.acquire(MAX_MESSAGE_BYTES).unwrap());
        }
        let selected = retained.pop().unwrap();
        assert!(budget.acquire(1).is_err());
        assert_eq!(*budget.0.lock().unwrap(), (8, MAX_INPUT_BYTES));
        drop(selected);
        let batch = budget.acquire(MAX_MESSAGE_BYTES).unwrap();
        assert!(budget.acquire(1).is_err());
        drop(batch);
        drop(retained);
        assert_eq!(*budget.0.lock().unwrap(), (0, 0));
        let retained: Vec<_> = (0..MAX_INPUT_ITEMS)
            .map(|_| budget.acquire(1).unwrap())
            .collect();
        assert!(budget.acquire(0).is_err());
        drop(retained);
        assert!(budget.acquire(MAX_MESSAGE_BYTES + 1).is_err());
        assert_eq!(*budget.0.lock().unwrap(), (0, 0));
    }

    #[test]
    // Verify background application failure and context removal finish receipts
    fn background_application_failure_and_context_removal_finish_receipts() {
        struct Background;
        impl Handler for Background {
            // Dispatch the received event to the registered behavior
            fn handle(&mut self, client: &Client, event: &Event) -> Result<()> {
                if event.event == "keyUp" {
                    client.background(
                        &event.context,
                        |_| Ok(json!(42)),
                        |_, _| Err(error("apply failed")),
                    )?;
                }
                Ok(())
            }
        }
        let fixture: Value =
            serde_json::from_str(include_str!("../testdata/protocol-v1.json")).unwrap();
        let (client, rx) = Client::new(
            HostInfo::parse(&fixture["manager"].to_string()).unwrap(),
            "fixture".into(),
        );
        let event = Event::parse(
            r#"{"event":"keyUp","context":"a","streamDockManager":{"request_id":"a1"}}"#,
        )
        .unwrap();
        client.dispatch(&mut Background, &event).unwrap();
        let deadline = Instant::now() + Duration::from_secs(2);
        let result = loop {
            client.drain_results();
            if let Ok(value) = rx.try_recv() {
                break value;
            }
            assert!(Instant::now() < deadline);
            std::thread::yield_now();
        };
        assert_eq!(
            result["payload"],
            json!({"request_id":"a1","success":false})
        );

        let (started, ready) = channel();
        let applied = Arc::new(AtomicBool::new(false));
        let output = applied.clone();
        let cancel = client
            .background(
                "a",
                move |cancel| {
                    started.send(()).unwrap();
                    while !cancel.load(Ordering::Acquire) {
                        std::thread::yield_now()
                    }
                    Ok(Value::Null)
                },
                move |_, _| {
                    output.store(true, Ordering::Release);
                    Ok(())
                },
            )
            .unwrap();
        ready.recv_timeout(Duration::from_secs(2)).unwrap();
        client
            .dispatch(
                &mut Background,
                &Event::parse(r#"{"event":"willDisappear","context":"a"}"#).unwrap(),
            )
            .unwrap();
        assert!(cancel.load(Ordering::Acquire));
        client.drain_results();
        assert!(!applied.load(Ordering::Acquire));
        client.close();
    }
}
