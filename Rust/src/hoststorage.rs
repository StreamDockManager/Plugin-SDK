//! Capability-negotiated host storage and portable action/global settings
//! No filesystem or SQLite dependency is used by these APIs
use crate::{error, Client, Event, Result, UnsupportedFeatureError};
use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};
use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc::{self, Receiver, SyncSender},
        Arc, Mutex,
    },
    time::{Duration, Instant},
};

pub const RPC_REQUEST_EVENT: &str = "streamDockManagerRpcRequest";
pub const RPC_RESPONSE_EVENT: &str = "streamDockManagerRpcResponse";
pub const STORAGE_PROTOCOL: &str = "plugin-storage-v1";
pub const SETTINGS_PROTOCOL: &str = "plugin-settings-rpc-v1";
pub const MAXIMUM_STORAGE_MESSAGE: usize = 512 << 10;
pub const MAXIMUM_STORAGE_VALUE: usize = 256 << 10;

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct RpcError {
    pub code: String,
    pub message: String,
}
impl std::fmt::Display for RpcError {
    // Format the error for its caller
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}: {}", self.code, self.message)
    }
}
impl std::error::Error for RpcError {}
// Construct a structured host RPC error
fn rpc_error(code: &str, message: &str) -> crate::Error {
    Box::new(RpcError {
        code: code.into(),
        message: message.into(),
    })
}

#[derive(Clone)]
pub struct RequestOptions {
    pub timeout: Duration,
    pub cancellation: Option<Arc<AtomicBool>>,
}
impl Default for RequestOptions {
    // Supply the default configuration without performing I/O for host-managed storage
    fn default() -> Self {
        Self {
            timeout: Duration::from_secs(5),
            cancellation: None,
        }
    }
}
impl RequestOptions {
    // Report whether cancellation has been requested
    fn cancelled(&self) -> bool {
        self.cancellation
            .as_ref()
            .is_some_and(|c| c.load(Ordering::Acquire))
    }
}

struct Pending {
    protocol: String,
    reply: SyncSender<std::result::Result<Value, RpcError>>,
}
#[derive(Default)]
pub(crate) struct Requests {
    rpc: HashMap<String, Pending>,
    settings: HashMap<String, Pending>,
}

// Generate a fresh operation identity for receipt correlation
pub fn operation_id() -> String {
    uuid::Uuid::new_v4().to_string()
}

impl Client {
    // Perform a bounded request through the configured transport for host-managed storage
    pub fn request(&self, protocol: &str, method: &str, payload: Value) -> Result<Value> {
        self.request_with_options(protocol, method, payload, &RequestOptions::default())
    }
    // Send a host RPC with explicit timeout and cancellation bounds
    pub fn request_with_options(
        &self,
        protocol: &str,
        method: &str,
        payload: Value,
        options: &RequestOptions,
    ) -> Result<Value> {
        if !self.supports(protocol) {
            return Err(Box::new(UnsupportedFeatureError(protocol.into())));
        }
        if options.cancelled() {
            return Err(rpc_error(
                "cancelled_before_commit",
                "request cancelled before enqueue",
            ));
        }
        let id = operation_id();
        self.ensure_request_open()?;
        let message = json!({"event":RPC_REQUEST_EVENT,"protocol_id":protocol,"request_id":id,"method":method,"payload":payload});
        if serde_json::to_vec(&message)?.len() > MAXIMUM_STORAGE_MESSAGE {
            return Err(rpc_error("quota", "storage request exceeds 512 KiB"));
        }
        let (reply, receiver) = mpsc::sync_channel(1);
        {
            let mut pending = self.inner.requests.lock().unwrap();
            if pending.rpc.len() + pending.settings.len() >= 8 {
                return Err(rpc_error(
                    "busy",
                    "eight storage requests are already pending",
                ));
            }
            pending.rpc.insert(
                id.clone(),
                Pending {
                    protocol: protocol.into(),
                    reply,
                },
            );
        }
        let outcome = self
            .enqueue_control(message)
            .and_then(|_| wait_response(receiver, options));
        self.inner.requests.lock().unwrap().rpc.remove(&id);
        outcome
    }

    /// The transport invokes this before normal event admission. It never invokes
    /// user code. Base settings observations still reach ordinary event handlers
    pub fn observe_control(&self, event: &Event) -> bool {
        self.observe_control_bytes(
            event,
            serde_json::to_vec(event).map_or(usize::MAX, |bytes| bytes.len()),
        )
    }

    // Correlate host responses before ordinary event admission
    pub(crate) fn observe_control_bytes(&self, event: &Event, encoded_bytes: usize) -> bool {
        let mut pending = self.inner.requests.lock().unwrap();
        if event.event == RPC_RESPONSE_EVENT {
            let id = event
                .extra
                .get("request_id")
                .and_then(Value::as_str)
                .unwrap_or("");
            let Some(request) = pending.rpc.remove(id) else {
                return true;
            };
            let protocol = event.extra.get("protocol_id").and_then(Value::as_str);
            let result = event.extra.get("result");
            let failure = event.extra.get("error");
            // Require correlated protocol identity, one outcome and the encoded response budget
            let response = if protocol != Some(request.protocol.as_str())
                || result.is_some() == failure.is_some()
                || encoded_bytes > MAXIMUM_STORAGE_MESSAGE
            {
                Err(RpcError {
                    code: "invalid_response".into(),
                    message: "invalid Manager RPC response".into(),
                })
            } else if let Some(failure) = failure {
                if failure.is_object()
                    && failure.get("code").is_some_and(Value::is_string)
                    && failure.get("message").is_some_and(Value::is_string)
                {
                    Err(RpcError {
                        code: failure["code"].as_str().unwrap().into(),
                        message: failure["message"].as_str().unwrap().into(),
                    })
                } else {
                    Err(RpcError {
                        code: "invalid_response".into(),
                        message: "invalid Manager RPC error".into(),
                    })
                }
            } else {
                Ok(result.cloned().unwrap_or(Value::Null))
            };
            let _ = request.reply.try_send(response);
            return true;
        }
        let key = match event.event.as_str() {
            "didReceiveGlobalSettings" => "global".into(),
            "didReceiveSettings" => format!("action:{}", event.context),
            _ => return false,
        };
        if let Some(request) = pending.settings.get(&key) {
            let response = event
                .payload
                .get("settings")
                .filter(|value| value.is_object())
                .cloned()
                .ok_or_else(|| RpcError {
                    code: "invalid_response".into(),
                    message: "invalid base settings response".into(),
                });
            let _ = request.reply.try_send(response);
        }
        false
    }

    // Fail pending host requests when their session closes
    pub(crate) fn cancel_requests(&self) {
        let mut pending = self.inner.requests.lock().unwrap();
        for (_, request) in pending.rpc.drain() {
            let _ = request.reply.try_send(Err(RpcError {
                code: "indeterminate".into(),
                message: "host disconnected; reconcile before retrying".into(),
            }));
        }
        for (_, request) in pending.settings.drain() {
            let _ = request.reply.try_send(Err(RpcError {
                code: "indeterminate".into(),
                message: "host disconnected; reconcile before retrying".into(),
            }));
        }
    }

    // Wait for a matching settings observation without swallowing ordinary events
    fn read_settings_observed(
        &self,
        scope: &str,
        address: &str,
        options: &RequestOptions,
    ) -> Result<Value> {
        if options.cancelled() {
            return Err(rpc_error(
                "cancelled_before_commit",
                "request cancelled before enqueue",
            ));
        }
        let key = if scope == "global" {
            "global".into()
        } else {
            format!("action:{address}")
        };
        self.ensure_request_open()?;
        let (reply, receiver) = mpsc::sync_channel(1);
        {
            let mut pending = self.inner.requests.lock().unwrap();
            if pending.settings.contains_key(&key)
                || pending.rpc.len() + pending.settings.len() >= 8
            {
                return Err(rpc_error(
                    "busy",
                    "settings read already pending or request capacity exhausted",
                ));
            }
            pending.settings.insert(
                key.clone(),
                Pending {
                    protocol: String::new(),
                    reply,
                },
            );
        }
        let sent = if scope == "global" {
            self.get_global_settings()
        } else {
            self.get_settings(address)
        };
        let result = sent.and_then(|_| wait_response(receiver, options));
        self.inner.requests.lock().unwrap().settings.remove(&key);
        result
    }
}

// Wait for a host reply within the deadline and cancellation bounds
fn wait_response(
    receiver: Receiver<std::result::Result<Value, RpcError>>,
    options: &RequestOptions,
) -> Result<Value> {
    let started = Instant::now();
    loop {
        if options.cancelled() {
            return Err(rpc_error(
                "indeterminate",
                "request cancelled after enqueue; reconcile before retrying",
            ));
        }
        let Some(remaining) = options.timeout.checked_sub(started.elapsed()) else {
            return Err(rpc_error(
                "indeterminate",
                "request timed out; reconcile before retrying",
            ));
        };
        match receiver.recv_timeout(remaining.min(Duration::from_millis(10))) {
            Ok(result) => return result.map_err(|e| Box::new(e) as crate::Error),
            Err(mpsc::RecvTimeoutError::Timeout) => {}
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                return Err(rpc_error(
                    "indeterminate",
                    "host disconnected; reconcile before retrying",
                ))
            }
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct StorageVersion {
    pub generation: String,
    #[serde(default)]
    pub revision: String,
}
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct StorageValue {
    #[serde(flatten)]
    pub version: StorageVersion,
    pub found: bool,
    #[serde(default)]
    pub value: Value,
}
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct StorageKeys {
    #[serde(flatten)]
    pub version: StorageVersion,
    pub keys: Vec<String>,
    #[serde(default)]
    pub cursor: Option<String>,
}
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct StorageReceipt {
    #[serde(flatten)]
    pub version: StorageVersion,
    pub operation_id: String,
    #[serde(default)]
    pub committed: bool,
    #[serde(default)]
    pub found: bool,
}
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
#[serde(tag = "op", rename_all = "lowercase")]
pub enum StorageMutation {
    Put { key: String, value: Value },
    Delete { key: String },
}
#[derive(Clone)]
pub struct HostStore {
    client: Client,
    namespace: String,
    options: RequestOptions,
}
impl HostStore {
    // Initialize the instance and its owned state for host-managed storage
    pub fn new(client: Client, namespace: impl Into<String>) -> Result<Self> {
        if !client.supports(STORAGE_PROTOCOL) {
            return Err(Box::new(UnsupportedFeatureError(STORAGE_PROTOCOL.into())));
        }
        let namespace = namespace.into();
        check_name(&namespace, 128, "namespace")?;
        Ok(Self {
            client,
            namespace,
            options: RequestOptions::default(),
        })
    }
    // Use explicit bounds for subsequent host requests
    pub fn with_request_options(mut self, options: RequestOptions) -> Self {
        self.options = options;
        self
    }
    // Invoke the host storage method and decode its typed result
    fn call<T: serde::de::DeserializeOwned>(&self, method: &str, mut payload: Value) -> Result<T> {
        payload["namespace"] = json!(self.namespace);
        let result = self.client.request_with_options(
            STORAGE_PROTOCOL,
            method,
            payload.clone(),
            &self.options,
        )?;
        validate_result(method, &payload, &result)?;
        serde_json::from_value(result)
            .map_err(|_| rpc_error("invalid_response", "invalid storage result"))
    }
    // Read the requested value through the current owner for host-managed storage
    pub fn get(&self, key: &str) -> Result<StorageValue> {
        check_name(key, 256, "key")?;
        self.call("get", json!({"key":key}))
    }
    // List storage keys using the supplied continuation cursor
    pub fn list_keys(&self, cursor: Option<&str>) -> Result<StorageKeys> {
        if cursor.is_some_and(|value| value.len() > 512) {
            return Err(error("cursor exceeds 512 bytes"));
        }
        let mut payload = json!({});
        if let Some(cursor) = cursor.filter(|v| !v.is_empty()) {
            payload["cursor"] = json!(cursor);
        }
        self.call("listKeys", payload)
    }
    // Submit the bounded storage transaction with its receipt identity
    pub fn commit(
        &self,
        version: &StorageVersion,
        operation: &str,
        mutations: &[StorageMutation],
    ) -> Result<StorageReceipt> {
        if !canonical_generation(&version.generation)
            || !canonical_revision(&version.revision)
            || !canonical_uuid(operation)
        {
            return Err(error(
                "canonical generation, revision and operation ID required",
            ));
        }
        if mutations.is_empty() || mutations.len() > 32 {
            return Err(error("commit requires 1–32 mutations"));
        }
        for mutation in mutations {
            match mutation {
                StorageMutation::Put { key, value } => {
                    check_name(key, 256, "key")?;
                    if serde_json::to_vec(value)?.len() > MAXIMUM_STORAGE_VALUE {
                        return Err(rpc_error("quota", "value exceeds 256 KiB"));
                    }
                }
                StorageMutation::Delete { key } => check_name(key, 256, "key")?,
            }
        }
        self.call("commit",json!({"generation":version.generation,"expected_revision":version.revision,"operation_id":operation,"mutations":mutations}))
    }
    // Look up a previous transaction receipt without replaying writes
    pub fn receipt(&self, generation: &str, operation: &str) -> Result<StorageReceipt> {
        if !canonical_generation(generation) || !canonical_uuid(operation) {
            return Err(error("canonical generation and operation ID required"));
        }
        self.call(
            "receipt",
            json!({"generation":generation,"operation_id":operation}),
        )
    }
}
// Validate a bounded storage name before sending it
fn check_name(value: &str, limit: usize, name: &str) -> Result<()> {
    if value.is_empty() || value.len() > limit {
        return Err(error(format!("{name} must be 1–{limit} UTF-8 bytes")));
    }
    Ok(())
}
// Accept only the canonical unsigned decimal representation
fn canonical_decimal(value: &str) -> bool {
    value == "0"
        || value
            .as_bytes()
            .first()
            .is_some_and(|v| (b'1'..=b'9').contains(v))
            && value.bytes().all(|b| b.is_ascii_digit())
}
// Validate the host generation identity
fn canonical_generation(value: &str) -> bool {
    value.len() <= 39 && canonical_decimal(value)
}
// Validate the host revision identity
fn canonical_revision(value: &str) -> bool {
    value.len() <= 20 && canonical_decimal(value) && value.parse::<u64>().is_ok()
}
// Check the method-specific shape of a host response
fn validate_result(method: &str, request: &Value, result: &Value) -> Result<()> {
    let invalid = || rpc_error("invalid_response", "invalid storage result");
    let object = result.as_object().ok_or_else(invalid)?;
    if !object
        .get("generation")
        .and_then(Value::as_str)
        .is_some_and(canonical_generation)
    {
        return Err(invalid());
    }
    let absent_receipt =
        method == "receipt" && object.get("found").and_then(Value::as_bool) == Some(false);
    if (!absent_receipt || object.contains_key("revision"))
        && !object
            .get("revision")
            .and_then(Value::as_str)
            .is_some_and(canonical_revision)
    {
        return Err(invalid());
    }
    let valid = match method {
        "get" => object
            .get("found")
            .and_then(Value::as_bool)
            .is_some_and(|found| found == object.contains_key("value")),
        "listKeys" => {
            object
                .get("keys")
                .and_then(Value::as_array)
                .is_some_and(|keys| {
                    keys.len() <= 128
                        && keys.iter().all(|key| {
                            key.as_str()
                                .is_some_and(|key| !key.is_empty() && key.len() <= 256)
                        })
                })
                && object
                    .get("cursor")
                    .is_none_or(|cursor| cursor.as_str().is_some_and(|s| s.len() <= 512))
        }
        "commit" => {
            object.get("committed").and_then(Value::as_bool) == Some(true)
                && object.get("operation_id") == request.get("operation_id")
        }
        "receipt" => {
            object
                .get("found")
                .and_then(Value::as_bool)
                .is_some_and(|found| {
                    !found || object.get("committed").and_then(Value::as_bool) == Some(true)
                })
                && object.get("operation_id") == request.get("operation_id")
        }
        _ => false,
    };
    if valid {
        Ok(())
    } else {
        Err(invalid())
    }
}
// Accept only the canonical operation UUID representation
fn canonical_uuid(value: &str) -> bool {
    uuid::Uuid::parse_str(value).is_ok_and(|id| id.to_string() == value)
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct SettingsResult {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub settings: Option<Map<String, Value>>,
    #[serde(default)]
    pub backend: String,
    pub assurance: String,
}
pub struct PortableSettings {
    client: Client,
    scope: String,
    address: String,
    options: RequestOptions,
    serial: Mutex<()>,
}
impl PortableSettings {
    // Initialize the instance and its owned state for host-managed storage
    pub fn new(client: Client, scope: &str, address: &str) -> Result<Self> {
        if scope != "global" && (scope != "action" || address.is_empty()) {
            return Err(error(
                "settings scope must be global or an owned action context",
            ));
        }
        Ok(Self {
            client,
            scope: scope.into(),
            address: address.into(),
            options: RequestOptions::default(),
            serial: Mutex::new(()),
        })
    }
    // Use explicit bounds for subsequent host requests
    pub fn with_request_options(mut self, options: RequestOptions) -> Self {
        self.options = options;
        self
    }
    // Read data through the owned transport or state backend for host-managed storage
    pub fn read(&self) -> Result<SettingsResult> {
        let _guard = self.serial.lock().unwrap();
        self.read_inner()
    }
    // Read settings using the selected enhanced or base protocol
    fn read_inner(&self) -> Result<SettingsResult> {
        if self.client.supports(SETTINGS_PROTOCOL) {
            match self.client.request_with_options(
                SETTINGS_PROTOCOL,
                "get",
                json!({"scope":self.scope,"context":self.address}),
                &self.options,
            ) {
                Ok(value) => {
                    if !value.is_object() {
                        return Err(rpc_error("invalid_response", "invalid settings response"));
                    }
                    let mut result: SettingsResult = serde_json::from_value(value)
                        .map_err(|_| rpc_error("invalid_response", "invalid settings response"))?;
                    if result.settings.is_none() || result.assurance != "observed" {
                        return Err(rpc_error("invalid_response", "invalid settings response"));
                    }
                    result.backend = SETTINGS_PROTOCOL.into();
                    return Ok(result);
                }
                Err(failure) if !is_unsupported(&failure) => return Err(failure),
                Err(_) => {}
            }
        }
        let settings = self
            .client
            .read_settings_observed(&self.scope, &self.address, &self.options)?
            .as_object()
            .cloned()
            .ok_or_else(|| error("settings response must be an object"))?;
        Ok(SettingsResult {
            settings: Some(settings),
            backend: "base-vsd".into(),
            assurance: "observed".into(),
        })
    }
    // Replace the settings snapshot under the selected revision
    pub fn replace(&self, settings: Map<String, Value>) -> Result<SettingsResult> {
        let _guard = self.serial.lock().unwrap();
        self.replace_inner(settings)
    }
    // Apply the settings replacement using the selected protocol
    fn replace_inner(&self, settings: Map<String, Value>) -> Result<SettingsResult> {
        if self.options.cancelled() {
            return Err(rpc_error(
                "cancelled_before_commit",
                "request cancelled before enqueue",
            ));
        }
        if self.client.supports(SETTINGS_PROTOCOL) {
            match self.client.request_with_options(
                SETTINGS_PROTOCOL,
                "replace",
                json!({"scope":self.scope,"context":self.address,"settings":settings}),
                &self.options,
            ) {
                Ok(value) => {
                    if !value.is_object() {
                        return Err(rpc_error("invalid_response", "invalid settings response"));
                    }
                    let mut result: SettingsResult = serde_json::from_value(value)
                        .map_err(|_| rpc_error("invalid_response", "invalid settings response"))?;
                    if !matches!(result.assurance.as_str(), "committed" | "applied_to_draft") {
                        return Err(rpc_error("invalid_response", "invalid settings response"));
                    }
                    result.backend = SETTINGS_PROTOCOL.into();
                    return Ok(result);
                }
                Err(failure) if !is_unsupported(&failure) => return Err(failure),
                Err(_) => {}
            }
        }
        if self.scope == "global" {
            self.client.global_settings(Value::Object(settings))?;
        } else {
            self.client
                .settings(&self.address, Value::Object(settings))?;
        }
        Ok(SettingsResult {
            settings: None,
            backend: "base-vsd".into(),
            assurance: "sent".into(),
        })
    }
    /// This facade serializes its own read/merge/write. Other writers can still
    /// race on base hosts; explicit null remains a value, not a deletion marker
    pub fn update(&self, changes: Map<String, Value>) -> Result<SettingsResult> {
        let _guard = self.serial.lock().unwrap();
        let mut current = self
            .read_inner()?
            .settings
            .ok_or_else(|| error("settings response must be an object"))?;
        current.extend(changes);
        self.replace_inner(current)
    }
}
// Distinguish explicit unsupported responses from malformed failures
fn is_unsupported(failure: &crate::Error) -> bool {
    failure
        .downcast_ref::<RpcError>()
        .is_some_and(|e| e.code == "unsupported")
}
