use crate::{error, Result};
use base64::Engine;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{
    collections::BTreeMap,
    time::{SystemTime, UNIX_EPOCH},
};

pub const CONTRACT_VERSION: u32 = 1;
pub const DESCRIPTOR_LIMIT: usize = 64 << 10;
pub const CAPABILITIES: &[&str] = &[
    "plugin-protocol-v1",
    "manager-controls-v1",
    "telemetry-v1",
    "surface-metadata-v1",
    "plugin-theme-v1",
    "plugin-settings-v1",
    "desktop-input-v1",
    "action-results-v1",
    "network-payload-v1",
    "plugin-logs-v1",
    "plugin-storage-v1",
    "plugin-settings-rpc-v1",
];

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Surface {
    pub control_id: String,
    pub width: u32,
    pub height: u32,
    pub feedback_target: String,
    pub request_id: String,
    pub touch: Option<Value>,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Event {
    pub event: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub context: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub action: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub device: String,
    #[serde(default, skip_serializing_if = "Value::is_null")]
    pub payload: Value,
    #[serde(
        default,
        rename = "streamDockManager",
        skip_serializing_if = "Option::is_none"
    )]
    pub surface: Option<Surface>,
    #[serde(flatten)]
    pub extra: BTreeMap<String, Value>,
}
impl Event {
    // Decode the incoming representation into the typed contract for host protocol metadata
    pub fn parse(raw: &str) -> Result<Self> {
        let event: Self = serde_json::from_str(raw)?;
        let arbitrary_payload = matches!(
            event.event.as_str(),
            "didReceiveSettings"
                | "didReceiveGlobalSettings"
                | "sendToPlugin"
                | "sendToPropertyInspector"
        );
        if event.event.is_empty()
            || (!arbitrary_payload && !(event.payload.is_null() || event.payload.is_object()))
        {
            return Err(error("host event needs a name and an object payload"));
        }
        if event.event == "didReceiveTelemetry"
            && event
                .payload
                .get("audio_observer")
                .and_then(Value::as_bool)
                .is_none()
        {
            return Err(error("telemetry audio_observer must be a boolean"));
        }
        Ok(event)
    }
    // Decode the pressed state of a control event
    pub fn pressed(&self) -> bool {
        self.payload
            .get("pressed")
            .or_else(|| self.payload.get("hold"))
            .and_then(Value::as_bool)
            .unwrap_or(false)
    }
    // Apply or retrieve the action settings in this scope for host protocol metadata
    pub fn settings(&self) -> &Value {
        &self.payload["settings"]
    }
    // Extract the device descriptor from the event
    pub fn device_info(&self) -> Option<&Value> {
        self.extra.get("deviceInfo")
    }
}
#[derive(Debug, Clone)]
pub struct HostInfo {
    pub raw: Value,
}
impl Default for HostInfo {
    // Supply the default configuration without performing I/O for host protocol metadata
    fn default() -> Self {
        Self {
            raw: serde_json::json!({}),
        }
    }
}
impl HostInfo {
    // Decode the incoming representation into the typed contract for host protocol metadata
    pub fn parse(raw: &str) -> Result<Self> {
        let value: Value = serde_json::from_str(raw)?;
        if !value.is_object() {
            return Err(error("host info must be a JSON object"));
        }
        Ok(Self { raw: value })
    }
    // Check whether the negotiated capability is available
    pub fn supports(&self, feature: &str) -> bool {
        let Some(manager) = self.raw.get("streamDockManager").and_then(Value::as_object) else {
            return false;
        };
        manager
            .get("contract_version")
            .is_some_and(|v| v.as_u64() == Some(1))
            && manager
                .get("capabilities")
                .and_then(Value::as_array)
                .is_some_and(|values| {
                    values.iter().all(Value::is_string)
                        && values.iter().any(|value| value.as_str() == Some(feature))
                })
    }
    // Read the negotiated host session identity
    pub fn run_id(&self) -> Option<&str> {
        self.raw["streamDockManager"]["run_id"]
            .as_str()
            .filter(|id| !id.is_empty() && id.len() <= 128)
    }
}
#[derive(Debug)]
pub struct UnsupportedFeatureError(pub String);
impl std::fmt::Display for UnsupportedFeatureError {
    // Format the error for its caller
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "host does not support {}", self.0)
    }
}
impl std::error::Error for UnsupportedFeatureError {}
// Map the outgoing event to its required capability
pub(crate) fn required_capability(event: &str) -> Option<&'static str> {
    match event {
        "subscribeTelemetry" => Some("telemetry-v1"),
        "desktopInput" => Some("desktop-input-v1"),
        "actionResult" => Some("action-results-v1"),
        "setDynamicItems" => Some("plugin-settings-v1"),
        "networkPayload" => Some("network-payload-v1"),
        _ => None,
    }
}
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct ThemedAsset {
    pub light: String,
    pub dark: String,
}
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct ActionCapability {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub refresh: Option<crate::polling::RefreshPolicy>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub parameter_fields: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub feedback_layout: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub control_types: Vec<String>,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub additive_dial: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub settings: Option<String>,
    pub icon: ThemedAsset,
    pub states: Vec<ThemedAsset>,
}
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct Capabilities {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub minimum_manager: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub required_capabilities: Vec<String>,
    pub version: u32,
    pub id: String,
    pub plugin_version: String,
    pub theme_protocol: u32,
    pub settings_protocol: u32,
    pub input_protocol: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub plugin_settings: Option<String>,
    pub icon: ThemedAsset,
    pub actions: BTreeMap<String, ActionCapability>,
}
// Select the capabilities represented by a frame
pub fn frame_capabilities(value: &Capabilities) -> Result<String> {
    let raw = serde_json::to_vec(value)?;
    if raw.len() > DESCRIPTOR_LIMIT {
        return Err(error("descriptor exceeds limit"));
    }
    let encoded = base64::engine::general_purpose::STANDARD.encode(raw);
    Ok(format!(
        "SDMCAP1:{:08x}:{}:ENDSDMCAP1",
        encoded.len(),
        encoded
    ))
}
// Validate advertised capabilities before negotiation
pub fn parse_capabilities(frame: &str) -> Result<Capabilities> {
    let raw = frame.as_bytes();
    if raw.len() < 17 || !raw.starts_with(b"SDMCAP1:") || raw[16] != b':' {
        return Err(error("invalid descriptor framing"));
    }
    let size = usize::from_str_radix(std::str::from_utf8(&raw[8..16])?, 16)?;
    if size > DESCRIPTOR_LIMIT * 2 || raw.get(17 + size..) != Some(b":ENDSDMCAP1".as_slice()) {
        return Err(error("invalid descriptor length"));
    }
    let data = base64::engine::general_purpose::STANDARD.decode(&raw[17..17 + size])?;
    if data.len() > DESCRIPTOR_LIMIT {
        return Err(error("descriptor exceeds limit"));
    }
    Ok(serde_json::from_slice(&data)?)
}
/// Retain and consume the same static descriptor bytes inspected by Manager
#[macro_export]
macro_rules! embed_capabilities {
    ($frame:expr) => {{
        #[used]
        static FRAME: &str = $frame;
        $crate::parse_capabilities(FRAME.trim_end())
    }};
}
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct Measurement {
    pub value: Option<f64>,
    pub observed_at: u64,
    pub max_age_ms: u64,
    pub error: Option<String>,
}
impl Measurement {
    // Check measurement freshness at the supplied timestamp
    pub fn fresh_at(&self, now_ms: u64) -> bool {
        self.value.is_some_and(f64::is_finite)
            && self.error.is_none()
            && now_ms
                .checked_sub(self.observed_at)
                .is_some_and(|age| age <= self.max_age_ms)
    }
    // Check measurement freshness against the current time
    pub fn fresh(&self) -> bool {
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .is_ok_and(|v| self.fresh_at(v.as_millis() as u64))
    }
}
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TelemetrySnapshot {
    #[serde(default)]
    pub cpu: Measurement,
    #[serde(default)]
    pub memory: Measurement,
    #[serde(default)]
    pub network: Measurement,
    #[serde(default)]
    pub disks: BTreeMap<String, Measurement>,
    #[serde(default)]
    pub audio_volume: Measurement,
    #[serde(default)]
    pub audio_muted: Measurement,
    pub audio_observer: bool,
}
