//! Optional action runtime built on the SDK's owned event loop
use crate::{error, Client, Deferred, Event, Handler, ProbePool, Result};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::{
    collections::{BTreeMap, BTreeSet},
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc::{sync_channel, Receiver, SyncSender},
        Arc,
    },
    time::{Instant, SystemTime, UNIX_EPOCH},
};

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
#[serde(default)]
pub struct Display {
    pub title: String,
    pub image: String,
    pub state: u32,
    pub layout: String,
    pub feedback: BTreeMap<String, Value>,
}

// Compare feedback without discarding numeric precision
fn feedback_equal(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Number(a), Value::Number(b)) => {
            a == b
                || matches!((decimal_parts(&a.to_string()), decimal_parts(&b.to_string())), (Some(a), Some(b)) if a == b)
        }
        (Value::Array(a), Value::Array(b)) => {
            a.len() == b.len() && a.iter().zip(b).all(|(a, b)| feedback_equal(a, b))
        }
        (Value::Object(a), Value::Object(b)) => {
            a.len() == b.len()
                && a.iter()
                    .all(|(key, a)| b.get(key).is_some_and(|b| feedback_equal(a, b)))
        }
        _ => a == b,
    }
}

// serde_json numbers have bounded exponents. Normalize decimal spelling rather
// than rounding large integers through f64 or allocating exponent-sized buffers
fn decimal_parts(value: &str) -> Option<(String, i64)> {
    let negative = value.starts_with('-');
    let value = value.trim_start_matches('-');
    let (mantissa, mut scale) = match value.split_once(['e', 'E']) {
        Some((mantissa, exponent)) => (mantissa, exponent.parse::<i64>().ok()?),
        None => (value, 0),
    };
    let mut digits = mantissa.to_string();
    if let Some(at) = digits.find('.') {
        scale = scale.checked_sub((digits.len() - at - 1).try_into().ok()?)?;
        digits.remove(at);
    }
    let digits = digits.trim_start_matches('0');
    if digits.is_empty() {
        return Some(("0".into(), 0));
    }
    let trimmed = digits.trim_end_matches('0');
    scale = scale.checked_add((digits.len() - trimmed.len()).try_into().ok()?)?;
    Some((
        format!("{}{trimmed}", if negative { "-" } else { "" }),
        scale,
    ))
}
// Translate a rendered display into protocol messages
pub fn display_messages(
    context: &str,
    previous: Option<&Display>,
    desired: &Display,
    rich: bool,
    states: bool,
) -> Vec<Value> {
    let empty = Display::default();
    let old = previous.unwrap_or(&empty);
    let known = previous.is_some();
    let layout = !known || old.layout != desired.layout;
    let state = states && (!known || old.state != desired.state);
    let mut messages = vec![];
    let mut send = |event: &str, payload: Value| {
        messages.push(json!({"event":event,"context":context,"payload":payload}))
    };
    if rich && layout && !desired.layout.is_empty() {
        send("setFeedbackLayout", json!({"layout":desired.layout}))
    }
    if state {
        send("setState", json!({"state":desired.state}))
    }
    if (!known && !desired.image.is_empty())
        || (known && old.image != desired.image)
        || (state && !desired.image.is_empty())
    {
        send("setImage", json!({"image":desired.image,"target":0}))
    }
    if !known || old.title != desired.title || state {
        send("setTitle", json!({"title":desired.title,"target":0}))
    }
    if rich {
        let mut feedback = BTreeMap::new();
        for (key, value) in &desired.feedback {
            if layout
                || !old
                    .feedback
                    .get(key)
                    .is_some_and(|old| feedback_equal(old, value))
            {
                feedback.insert(key, value.clone());
            }
        }
        for key in old.feedback.keys() {
            if !layout && !desired.feedback.contains_key(key) {
                feedback.insert(key, Value::Null);
            }
        }
        if !feedback.is_empty() {
            send("setFeedback", json!(feedback))
        }
    }
    messages
}
#[derive(Default)]
pub struct Renderer {
    pub last: BTreeMap<String, Display>,
}
impl Renderer {
    // Reset retained state for the next lifecycle for action authoring
    pub fn reset(&mut self, context: Option<&str>) {
        if let Some(key) = context {
            self.last.remove(key);
        } else {
            self.last.clear()
        }
    }
    // Produce the current display representation for action authoring
    pub fn render(&mut self, client: &Client, context: &str, desired: Display) -> Result<()> {
        for message in display_messages(context, self.last.get(context), &desired, true, true) {
            client.send(
                message["event"].as_str().unwrap(),
                context,
                message["payload"].clone(),
            )?
        }
        self.last.insert(context.into(), desired);
        Ok(())
    }
}
#[derive(Clone, Debug)]
pub struct Binding {
    pub action: String,
    pub settings: Value,
    pub surface: Option<crate::Surface>,
}
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct ViewTarget {
    pub action: String,
    pub updated_by: Vec<String>,
    pub shared: bool,
    pub live: bool,
    pub after_activation: bool,
}
#[derive(Clone, Default)]
pub struct Scope {
    pub origin: String,
    pub action: String,
    pub automatic: bool,
    pub lifecycle: bool,
}
// Check whether the existing view may accept this update
pub fn can_update(
    bindings: &BTreeMap<String, Binding>,
    targets: &BTreeMap<String, ViewTarget>,
    active: &BTreeSet<String>,
    context: &str,
    scope: &Scope,
    globals: &Value,
) -> bool {
    let Some(binding) = bindings.get(context) else {
        return false;
    };
    let default = ViewTarget {
        action: binding.action.clone(),
        live: true,
        ..Default::default()
    };
    let rule = targets.get(&binding.action).unwrap_or(&default);
    let live = rule.live && (!rule.after_activation || active.contains(context));
    if !scope.origin.is_empty()
        && !scope.automatic
        && !scope.action.is_empty()
        && !bindings
            .get(&scope.origin)
            .is_some_and(|b| b.action == scope.action)
    {
        return false;
    }
    if scope.origin == context {
        return !scope.lifecycle || live || active.contains(context);
    }
    if scope.automatic {
        return live;
    }
    if scope.origin.is_empty()
        || !rule.updated_by.contains(&scope.action)
        || rule.after_activation && !active.contains(context)
    {
        return false;
    }
    if rule.shared {
        let Some(source) = bindings.get(&scope.origin) else {
            return false;
        };
        let a = crate::settings::merge(globals, &source.settings);
        let b = crate::settings::merge(globals, &binding.settings);
        return a["shared_id"]
            .as_str()
            .is_some_and(|v| !v.is_empty() && b["shared_id"] == v);
    }
    scope.action != binding.action
}
// Choose artwork for the active theme
pub fn theme_asset(value: &Value, theme: &str) -> Value {
    match value {
        Value::String(text) => {
            let mut out = text.clone();
            for extension in [".svg", ".json"] {
                for prior in ["light", "dark"] {
                    let suffix = format!("-{prior}{extension}");
                    if text.ends_with(&suffix) {
                        out = format!("{}-{theme}{extension}", text.trim_end_matches(&suffix))
                    }
                }
            }
            json!(out)
        }
        Value::Object(map) => Value::Object(
            map.iter()
                .map(|(k, v)| (k.clone(), theme_asset(v, theme)))
                .collect(),
        ),
        Value::Array(values) => {
            Value::Array(values.iter().map(|v| theme_asset(v, theme)).collect())
        }
        _ => value.clone(),
    }
}
pub trait Action {
    // Dispatch the received event to the registered behavior for action authoring
    fn handle(&mut self, _plugin: &mut Plugin, _client: &Client, _event: &Event) -> Result<()> {
        Ok(())
    }
    // Register the newly visible action instance
    fn appeared(&mut self, _plugin: &mut Plugin, _client: &Client, _event: &Event) -> Result<()> {
        Ok(())
    }
    // Refresh state after an observed change
    fn changed(&mut self, _plugin: &mut Plugin, _client: &Client, _event: &Event) -> Result<()> {
        Ok(())
    }
    // Remove the action instance that is no longer visible
    fn disappeared(
        &mut self,
        _plugin: &mut Plugin,
        _client: &Client,
        _event: &Event,
    ) -> Result<()> {
        Ok(())
    }
    // Produce the current display representation for action authoring
    fn render(
        &mut self,
        _plugin: &mut Plugin,
        _context: &str,
        _binding: &Binding,
    ) -> Result<Option<Display>> {
        Ok(None)
    }
}
type Apply = Box<dyn FnOnce(&mut Plugin, &Client, Result<Value>) -> Result<()> + Send>;
struct Job {
    context: String,
    generation: u64,
    cancel: Arc<AtomicBool>,
    complete: Deferred,
    apply: Apply,
    scope: Scope,
}
struct JobResult {
    key: String,
    generation: u64,
    value: Result<Value>,
}
pub type Inspector = Box<dyn FnMut(&mut Plugin, &Client, &Event) -> Result<()>>;
pub struct Plugin {
    pub action_logging: Option<fn(&Event) -> bool>,
    pub inspector: Option<Inspector>,
    pub clock: Box<dyn Fn() -> u64>,
    pub bindings: BTreeMap<String, Binding>,
    pub globals: Value,
    pub active: BTreeSet<String>,
    pub targets: BTreeMap<String, ViewTarget>,
    pub renderer: Renderer,
    pub store: Option<crate::storage::Store>,
    actions: BTreeMap<String, Box<dyn Action>>,
    scope: Scope,
    jobs: BTreeMap<String, Job>,
    sequence: u64,
    pool: ProbePool,
    sender: SyncSender<JobResult>,
    receiver: Receiver<JobResult>,
    closed: bool,
}
impl Default for Plugin {
    // Supply the default configuration without performing I/O for action authoring
    fn default() -> Self {
        let (sender, receiver) = sync_channel(64);
        Self {
            action_logging: None,
            inspector: None,
            clock: Box::new(|| {
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap_or_default()
                    .as_millis() as u64
            }),
            bindings: BTreeMap::new(),
            globals: json!({}),
            active: BTreeSet::new(),
            targets: BTreeMap::new(),
            renderer: Renderer::default(),
            store: None,
            actions: BTreeMap::new(),
            scope: Scope {
                automatic: true,
                ..Default::default()
            },
            jobs: BTreeMap::new(),
            sequence: 0,
            pool: ProbePool::default(),
            sender,
            receiver,
            closed: false,
        }
    }
}
impl Plugin {
    // Check whether the action identity has a handler
    pub fn is_registered(&self, action: &str) -> bool {
        self.actions.contains_key(action)
    }
    // Register the declared handler or device identity
    pub fn register(&mut self, action: &str, handler: impl Action + 'static) -> Result<()> {
        if action.is_empty() || self.actions.contains_key(action) {
            return Err(error("empty or duplicate action"));
        };
        self.actions.insert(action.into(), Box::new(handler));
        Ok(())
    }
    // Merge the settings used by this action instance
    pub fn effective_settings(&self, context: &str) -> Value {
        crate::settings::merge(
            &self.globals,
            &self
                .bindings
                .get(context)
                .map(|v| v.settings.clone())
                .unwrap_or(json!({})),
        )
    }
    // Produce the current display representation for action authoring
    pub fn render(&mut self, client: &Client, context: &str) -> Result<()> {
        if !can_update(
            &self.bindings,
            &self.targets,
            &self.active,
            context,
            &self.scope,
            &self.globals,
        ) {
            return Ok(());
        }
        let binding = self.bindings[context].clone();
        if let Some(mut action) = self.actions.remove(&binding.action) {
            let result = action.render(self, context, &binding);
            self.actions.insert(binding.action, action);
            if let Some(view) = result? {
                self.renderer.render(client, context, view)?
            }
        };
        Ok(())
    }
    // Render the action instances affected by this update
    pub fn render_targets(&mut self, client: &Client) -> Result<()> {
        let keys: Vec<_> = self.bindings.keys().cloned().collect();
        for key in keys {
            self.render(client, &key)?
        }
        Ok(())
    }
    // Cancel work belonging to the selected action context
    pub fn cancel_job(&mut self, key: &str) {
        if let Some(job) = self.jobs.remove(key) {
            job.cancel.store(true, Ordering::Release);
            drop(job)
        }
    }
    // Discard retained state and reset its bookkeeping
    fn clear(&mut self, context: &str) {
        let jobs: Vec<_> = self
            .jobs
            .iter()
            .filter(|(_, v)| v.context == context)
            .map(|(k, _)| k.clone())
            .collect();
        for key in jobs {
            self.cancel_job(&key)
        }
        self.renderer.reset(Some(context))
    }
    // Submit cancellable work outside the owner event loop
    pub fn background(
        &mut self,
        client: &Client,
        key: &str,
        context: &str,
        replace: bool,
        work: impl FnOnce(Arc<AtomicBool>) -> Result<Value> + Send + 'static,
        apply: impl FnOnce(&mut Plugin, &Client, Value) -> Result<()> + Send + 'static,
    ) -> Result<Arc<AtomicBool>> {
        self.start_background(
            client,
            key,
            context,
            replace,
            true,
            work,
            move |plugin, client, value| value.and_then(|value| apply(plugin, client, value)),
        )
    }
    /// A read whose admission, failure or cancellation never completes an action
    /// The apply callback receives the read error and retains the initiating view scope
    pub fn observe(
        &mut self,
        client: &Client,
        key: &str,
        context: &str,
        work: impl FnOnce(Arc<AtomicBool>) -> Result<Value> + Send + 'static,
        apply: impl FnOnce(&mut Plugin, &Client, Result<Value>) -> Result<()> + Send + 'static,
    ) -> Option<Arc<AtomicBool>> {
        self.start_background(client, key, context, false, false, work, apply)
            .ok()
    }
    #[allow(clippy::too_many_arguments)]
    // Start background work with its lifecycle ownership
    fn start_background(
        &mut self,
        client: &Client,
        key: &str,
        context: &str,
        replace: bool,
        track_action: bool,
        work: impl FnOnce(Arc<AtomicBool>) -> Result<Value> + Send + 'static,
        apply: impl FnOnce(&mut Plugin, &Client, Result<Value>) -> Result<()> + Send + 'static,
    ) -> Result<Arc<AtomicBool>> {
        if self.closed {
            return Err(error("plugin closed"));
        };
        if self.jobs.contains_key(key) {
            if !replace {
                return Err(error("plugin action already running"));
            };
            self.cancel_job(key)
        };
        if self.jobs.len() >= 32 {
            return Err(error("plugin job queue full"));
        }
        self.sequence += 1;
        let generation = self.sequence;
        let cancel = Arc::new(AtomicBool::new(false));
        let flag = cancel.clone();
        let sender = self.sender.clone();
        let job_key = key.to_owned();
        self.jobs.insert(
            key.into(),
            Job {
                context: context.into(),
                generation,
                cancel: cancel.clone(),
                complete: if track_action {
                    client.defer_action()
                } else {
                    Deferred { action: None }
                },
                apply: Box::new(apply),
                scope: self.scope.clone(),
            },
        );
        if !self.pool.submit(cancel.clone(), move || {
            let value =
                std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| work(flag.clone())))
                    .unwrap_or_else(|_| Err(error("background job panicked")));
            if !flag.load(Ordering::Acquire) {
                let _ = sender.try_send(JobResult {
                    key: job_key,
                    generation,
                    value,
                });
            }
        }) {
            self.cancel_job(key);
            return Err(error("plugin job queue full"));
        };
        client.wake();
        Ok(cancel)
    }
    // Apply finished jobs on the owner loop
    pub fn drain_jobs(&mut self, client: &Client) -> Result<()> {
        while let Ok(result) = self.receiver.try_recv() {
            if !self
                .jobs
                .get(&result.key)
                .is_some_and(|job| job.generation == result.generation)
            {
                continue;
            };
            let job = self.jobs.remove(&result.key).unwrap();
            let old = std::mem::replace(&mut self.scope, job.scope);
            let applied = if job.cancel.load(Ordering::Acquire) {
                Err(error("background job cancelled"))
            } else {
                (job.apply)(self, client, result.value).and_then(|_| self.render_targets(client))
            };
            self.scope = old;
            job.complete.finish(applied);
        }
        Ok(())
    }
    // Invoke the configured action callback
    fn callback(&mut self, client: &Client, event: &Event, action: &str) -> Result<()> {
        if let Some(mut handler) = self.actions.remove(action) {
            let result = match event.event.as_str() {
                "willAppear" => handler.appeared(self, client, event),
                "didReceiveSettings" => handler.changed(self, client, event),
                "willDisappear" => handler.disappeared(self, client, event),
                _ => handler.handle(self, client, event),
            };
            self.actions.insert(action.into(), handler);
            result
        } else {
            Ok(())
        }
    }
    // Route an event through the action lifecycle
    fn event(&mut self, client: &Client, event: &Event) -> Result<()> {
        if event.event == "sendToPlugin" {
            if let Some(mut inspector) = self.inspector.take() {
                let result = inspector(self, client, event);
                self.inspector = Some(inspector);
                return result;
            }
        }

        if event.event == "didReceiveGlobalSettings" {
            self.globals = event.settings().clone();
            let keys: Vec<_> = self.jobs.keys().cloned().collect();
            for key in keys {
                self.cancel_job(&key)
            }
            return Ok(());
        }
        if event.event == crate::THEME_EVENT {
            let theme = event.payload["resolved"]
                .as_str()
                .filter(|v| ["light", "dark"].contains(v))
                .ok_or_else(|| error("unsupported theme"))?;
            let previous = std::mem::take(&mut self.renderer.last);
            for (key, mut view) in previous {
                view.image = theme_asset(&json!(view.image), theme)
                    .as_str()
                    .unwrap()
                    .into();
                view.layout = theme_asset(&json!(view.layout), theme)
                    .as_str()
                    .unwrap()
                    .into();
                view.feedback = serde_json::from_value(theme_asset(&json!(view.feedback), theme))?;
                self.renderer.render(client, &key, view)?
            }
            return Ok(());
        }
        if event.context.is_empty() {
            return Ok(());
        }
        let lifecycle = matches!(event.event.as_str(), "willAppear" | "didReceiveSettings");
        let old = self.bindings.get(&event.context).cloned();
        if lifecycle && old.as_ref().is_some_and(|v| v.action != event.action) {
            self.clear(&event.context);
            self.active.remove(&event.context);
            let mut gone = event.clone();
            gone.event = "willDisappear".into();
            self.callback(client, &gone, &old.as_ref().unwrap().action)?
        }
        let action = if event.action.is_empty() {
            old.as_ref().map(|v| v.action.clone()).unwrap_or_default()
        } else {
            event.action.clone()
        };
        if event.event == "willDisappear" {
            self.clear(&event.context);
            self.bindings.remove(&event.context);
            self.active.remove(&event.context);
            return self.callback(client, event, &action);
        }
        let touch = event.surface.as_ref().and_then(|s| s.touch.as_ref());
        let activated = matches!(event.event.as_str(), "keyDown" | "dialDown")
            || event.event == "touchTap"
                && touch.map_or_else(
                    || !event.payload["hold"].as_bool().unwrap_or(false),
                    |t| matches!(t["phase"].as_str().unwrap_or(""), "tap" | "end" | ""),
                );
        self.scope = Scope {
            origin: event.context.clone(),
            action: action.clone(),
            automatic: lifecycle,
            lifecycle,
        };
        if lifecycle {
            if event.event == "didReceiveSettings" {
                self.clear(&event.context)
            };
            self.bindings.insert(
                event.context.clone(),
                Binding {
                    action: action.clone(),
                    settings: event.settings().clone(),
                    surface: event
                        .surface
                        .clone()
                        .or_else(|| old.and_then(|v| v.surface)),
                },
            );
        } else if matches!(
            event.event.as_str(),
            "keyDown" | "keyUp" | "dialDown" | "dialUp" | "dialRotate" | "touchTap"
        ) && !self.bindings.contains_key(&event.context)
        {
            self.bindings.insert(
                event.context.clone(),
                Binding {
                    action: action.clone(),
                    settings: event.settings().clone(),
                    surface: event.surface.clone(),
                },
            );
        }
        if activated {
            self.active.insert(event.context.clone());
        };
        self.callback(client, event, &action)?;
        if lifecycle {
            self.render(client, &event.context)?;
        } else if matches!(
            event.event.as_str(),
            "keyDown" | "keyUp" | "dialDown" | "dialUp" | "dialRotate" | "touchTap"
        ) {
            self.render_targets(client)?;
        };
        Ok(())
    }
}
impl Handler for Plugin {
    // Decide whether this action event belongs in diagnostic logs
    fn should_log_action(&self, event: &Event) -> bool {
        self.action_logging.unwrap_or(crate::should_log_action)(event)
    }
    // Dispatch the received event to the registered behavior for action authoring
    fn handle(&mut self, client: &Client, event: &Event) -> Result<()> {
        if [
            "willAppear",
            "didReceiveSettings",
            "didReceiveGlobalSettings",
        ]
        .contains(&event.event.as_str())
        {
            let mut snapshots = vec![self.globals.clone(), event.settings().clone()];
            snapshots.extend(
                self.bindings
                    .values()
                    .map(|binding| binding.settings.clone()),
            );
            client.set_log_redactor(move |text| {
                snapshots
                    .iter()
                    .fold(text.to_string(), |message, settings| {
                        crate::credentials::safe_error(crate::error(message), settings, &[])
                            .to_string()
                    })
            });
        }
        self.drain_jobs(client)?;
        let old = self.scope.clone();
        let result = self.event(client, event);
        self.scope = old;
        result
    }
    // Advance scheduled work on the owning event loop for action authoring
    fn poll(&mut self, client: &Client) -> Result<()> {
        self.drain_jobs(client)?;
        self.scope = Scope {
            automatic: true,
            ..Default::default()
        };
        self.render_targets(client)?;
        if let Some(store) = &mut self.store {
            let now = (self.clock)();
            store.flush(now, false)?;
        };
        Ok(())
    }
    // Return the next deadline that requires owner-loop attention
    fn next_poll(&self, now: Instant) -> Option<Instant> {
        let mut delays = Vec::new();
        if !self.jobs.is_empty() {
            delays.push(50);
        }
        if let Some(store) = &self.store {
            if store.dirty {
                delays.push(store.due.unwrap_or(0).saturating_sub((self.clock)()));
            }
        }
        if self.targets.values().any(|r| r.live) {
            delays.push(1000);
        }
        delays
            .into_iter()
            .min()
            .map(|delay| now + std::time::Duration::from_millis(delay))
    }
    // Close the owned session and release pending work for action authoring
    fn close(&mut self) -> Result<()> {
        self.closed = true;
        let jobs = std::mem::take(&mut self.jobs);
        for (_, job) in jobs {
            job.cancel.store(true, Ordering::Release)
        }
        self.pool.close();
        if let Some(store) = &mut self.store {
            store.close()?;
        };
        Ok(())
    }
}

#[derive(Clone, Debug, Default)]
pub struct Visual {
    pub icon: String,
    pub states: Vec<String>,
}
#[derive(Clone, Debug, Default)]
pub struct DisplayStatus {
    pub text: String,
    pub icon: String,
    pub state: u32,
    pub unavailable: bool,
}
// Construct a display from ordered text lines
pub fn display_lines(text: &str, columns: usize) -> Vec<String> {
    let columns = columns.max(1);
    let paragraphs: Vec<_> = text.split('\n').collect();
    let mut lines = vec![];
    for paragraph in &paragraphs {
        let mut chars: Vec<_> = paragraph.chars().collect();
        if paragraphs.len() > 1 && chars.len() > columns {
            lines.push(chars[..columns - 1].iter().collect::<String>() + ">");
            continue;
        }
        while chars.len() > columns {
            let end = chars[..columns]
                .iter()
                .rposition(|c| *c == ' ')
                .filter(|p| *p > 0)
                .unwrap_or(columns);
            lines.push(chars[..end].iter().collect());
            chars = chars[end..]
                .iter()
                .collect::<String>()
                .trim()
                .chars()
                .collect();
            if lines.len() > 3 {
                break;
            }
        }
        lines.push(chars.iter().collect());
        if lines.len() > 3 {
            break;
        }
    }
    if lines.len() > 3 {
        lines.truncate(3);
        let last: String = lines[2].chars().take(columns - 1).collect();
        lines[2] = last + ">"
    };
    lines
}
// Construct a display containing a status line
pub fn status_display(
    status: &DisplayStatus,
    surface: Option<&crate::Surface>,
    visual: &Visual,
    theme: &str,
) -> Display {
    let mut state = if (status.state as usize) < visual.states.len() {
        status.state
    } else {
        0
    };
    let mut icon = if visual.states.len() > 1 {
        visual.states[state as usize].clone()
    } else {
        visual.icon.clone()
    };
    if !status.icon.is_empty() {
        icon = status.icon.clone()
    };
    if status.unavailable || icon.is_empty() {
        icon = "icons/unavailable".into();
        state = 0
    };
    let suffix = if theme.is_empty() {
        String::new()
    } else {
        format!("-{theme}")
    };
    let image = format!("{icon}{suffix}.svg");
    let (width, height) = surface
        .filter(|v| v.width > 0 && v.height > 0)
        .map(|v| (v.width.min(16384), v.height.min(16384)))
        .unwrap_or((112, 112));
    let mut text_width = width * 192 / 200;
    let mut size = (width * 60 / 100).min(height * 43 / 100);
    let (mut x, mut y) = ((width - size) / 2, 0);
    let mut layout = "key";
    if width > height * 13 / 10 {
        layout = "wide";
        text_width = width * 120 / 200;
        size = (width * 36 / 100).min(height * 80 / 100);
        x = width * 2 / 100;
        y = (height - size) / 2
    }
    let title: String = status.text.chars().take(500).collect();
    let mut feedback = BTreeMap::new();
    feedback.insert(
        "icon".into(),
        json!({"value":image,"rect":[x*200/width,y*100/height,size*200/width,size*100/height]}),
    );
    feedback.insert("indicator".into(), json!({"rect":[0,0,0,0]}));
    let columns = (text_width / (8 * (height / 100).clamp(1, 6))).max(1) as usize;
    let lines = display_lines(&title, columns);
    for index in 0..3 {
        feedback.insert(
            format!("line{index}"),
            json!(lines.get(index).cloned().unwrap_or_default()),
        );
    }
    Display {
        title,
        image,
        state,
        layout: format!("display/{layout}{suffix}.json"),
        feedback,
    }
}
// Build the requested semantic display layout
pub fn display_layout(wide: bool) -> Value {
    serde_json::from_str(if wide {
        include_str!("../web/display/wide.json")
    } else {
        include_str!("../web/display/key.json")
    })
    .expect("SDK layout fixture")
}

/// Semantic content independent of a particular key's pixel geometry
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Presentation {
    pub kind: String,
    pub primary: String,
    pub label: String,
    pub detail: String,
    pub progress: f64,
    pub show_progress: bool,
}
// Translate presentation options into a display
pub fn present(view: &Display, content: &Presentation, width: u32, height: u32) -> Display {
    let mut view = view.clone();
    if content.kind.is_empty() || width == 0 || height == 0 || width > 16384 || height > 16384 {
        return view;
    }
    let primary = &content.primary;
    let label = if content.label == *primary {
        ""
    } else {
        &content.label
    };
    let detail = if content.detail == *primary || content.detail == label {
        ""
    } else {
        &content.detail
    };
    let wide = width > height * 13 / 10;
    let field = |value: &str, mut rect: [u32; 4], size: u32| {
        if value.is_empty() {
            rect[2] = 0;
            rect[3] = 0;
        }
        json!({"value":value,"rect":rect,"font":{"size":size,"fit":true}})
    };
    let icon = |size: u32| json!({"value":view.image,"rect":[4,(100-size*100/height)/2,size*200/width,size*100/height]});
    let feedback = &mut view.feedback;
    if wide {
        feedback.insert(
            "icon".into(),
            icon((width * 24 / 100).min(height * 70 / 100)),
        );
        feedback.insert("line0".into(), field(primary, [58, 8, 138, 48], 32));
        feedback.insert("line1".into(), field(label, [58, 58, 138, 18], 13));
        feedback.insert("line2".into(), field(detail, [58, 78, 138, 16], 11));
    } else {
        feedback.insert(
            "icon".into(),
            json!({"value":view.image,"rect":[76,2,48,24]}),
        );
        feedback.insert("line0".into(), field(primary, [6, 28, 188, 40], 30));
        feedback.insert("line1".into(), field(label, [6, 70, 188, 14], 11));
        feedback.insert("line2".into(), field(detail, [6, 85, 188, 11], 9));
    }
    if content.kind == "command" && primary.is_empty() {
        if wide {
            feedback.insert(
                "icon".into(),
                icon((width * 36 / 100).min(height * 80 / 100)),
            );
            feedback.insert("line1".into(), field(label, [82, 20, 114, 60], 24));
        } else {
            feedback.insert(
                "icon".into(),
                json!({"value":view.image,"rect":[45,5,110,55]}),
            );
            feedback.insert("line1".into(), field(label, [6, 67, 188, 26], 16));
        }
    }
    feedback.insert("indicator".into(), json!({"rect":[0,0,0,0]}));
    if content.show_progress && content.progress.is_finite() {
        feedback.insert(
            "indicator".into(),
            json!({"value":content.progress.clamp(0.,100.),"rect":[6,97,188,3]}),
        );
    }
    view
}
/// Enlarge the primary value on small square surfaces, preserving the input
pub fn compact_display(view: &Display, width: u32, height: u32) -> Display {
    let mut view = view.clone();
    if width == 0 || height == 0 || height >= 80 || width > height * 13 / 10 {
        return view;
    }
    let f = &mut view.feedback;
    if !f
        .get("line0")
        .is_some_and(|v| v.is_object() && !v["value"].is_null() && v["value"] != "")
        || !f.get("line1").is_some_and(Value::is_object)
        || !f.get("icon").is_some_and(Value::is_object)
    {
        return view;
    }
    let size = width.min(height) * 37 / 100;
    f.insert("line2".into(), json!({"rect":[0,0,0,0]}));
    f.get_mut("icon").unwrap()["rect"] = json!([
        (width - size) * 100 / width,
        2,
        size * 200 / width,
        size * 100 / height
    ]);
    f.get_mut("line0").unwrap()["rect"] = json!([7, 40, 186, 34]);
    f.get_mut("line1").unwrap()["rect"] = json!([7, 75, 186, 21]);
    f.get_mut("line1").unwrap()["font"] = json!({"size":19,"fit":true});
    view
}
