//! Opt-in OpenDeck device-provider envelopes; no transmission, grants or hardware access
//! Reference: nekename/OpenDeck 3ddecc1ce8952a423916cf066724356267b97410
use crate::{error, Result};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

pub struct OpenActionDeviceProvider {
    namespace: String,
}
#[derive(Clone, Debug, Serialize)]
pub struct OpenActionDeviceInfo {
    pub id: String,
    pub name: String,
    pub rows: u8,
    pub columns: u8,
    pub encoders: u8,
    pub touchpoints: u8,
    pub infobars: u8,
    #[serde(rename = "type")]
    pub device_type: u8,
}
#[derive(Debug, Deserialize)]
pub struct OpenActionDeviceOutput {
    pub event: String,
    pub device: String,
    pub controller: Option<String>,
    pub position: Option<u8>,
    pub image: Option<String>,
    pub brightness: Option<u8>,
}
impl OpenActionDeviceProvider {
    // Initialize the instance and its owned state for OpenAction device providers
    pub fn new(namespace: &str) -> Result<Self> {
        if namespace.len() != 2 || !namespace.bytes().all(|b| (33..=126).contains(&b)) {
            return Err(error(
                "DeviceNamespace must be two printable ASCII characters",
            ));
        }
        Ok(Self {
            namespace: namespace.into(),
        })
    }
    // Validate the provider identity before emitting events
    fn check(&self, device: &str) -> Result<()> {
        if device.len() > 256 || !device.starts_with(&self.namespace) {
            return Err(error(
                "device must belong to the explicitly selected namespace",
            ));
        }
        Ok(())
    }
    // Register the declared handler or device identity
    pub fn register(&self, info: OpenActionDeviceInfo) -> Result<Value> {
        self.check(&info.id)?;
        if info.name.is_empty() || info.name.len() > 1024 {
            return Err(error("device name must contain 1..1024 bytes"));
        }
        Ok(json!({"event":"registerDevice","payload":info}))
    }
    // Build a device-scoped OpenAction event
    pub fn device_event(&self, event: &str, device: &str) -> Result<Value> {
        self.check(device)?;
        if !matches!(event, "deregisterDevice" | "rerenderImages") {
            return Err(error("unsupported device event"));
        }
        Ok(json!({"event":event,"payload":device}))
    }
    // Emit a device button transition
    pub fn press(&self, event: &str, device: &str, position: u8) -> Result<Value> {
        self.check(device)?;
        if !matches!(event, "keyDown" | "keyUp" | "encoderDown" | "encoderUp") {
            return Err(error("unsupported press event"));
        }
        Ok(json!({"event":event,"payload":{"device":device,"position":position}}))
    }
    // Emit a device encoder delta
    pub fn encoder_change(&self, device: &str, position: u8, ticks: i16) -> Result<Value> {
        self.check(device)?;
        Ok(
            json!({"event":"encoderChange","payload":{"device":device,"position":position,"ticks":ticks}}),
        )
    }
    // Emit a device touchscreen press
    pub fn touchscreen_press(
        &self,
        device: &str,
        position: u8,
        x: u16,
        y: u16,
        hold: bool,
    ) -> Result<Value> {
        self.check(device)?;
        Ok(
            json!({"event":"touchscreenPress","payload":{"device":device,"position":position,"x":x,"y":y,"hold":hold}}),
        )
    }
    /// Image remains opaque; parsing does not fetch URIs, decode images or write hardware
    pub fn parse_output(&self, data: &[u8]) -> Result<OpenActionDeviceOutput> {
        if data.len() > 1 << 20 {
            return Err(error("device output exceeds message bound"));
        }
        let out: OpenActionDeviceOutput = serde_json::from_slice(data)?;
        self.check(&out.device)?;
        match out.event.as_str() {
            "setImage" => {
                if out.controller.is_some() != out.position.is_some()
                    || (out.controller.is_none() && out.image.is_some())
                {
                    return Err(error("invalid image controller/position"));
                }
            }
            "setBrightness" => {
                if out.brightness.is_none() {
                    return Err(error("brightness required"));
                }
            }
            _ => return Err(error("unsupported device output")),
        }
        Ok(out)
    }
}

impl OpenActionDeviceProvider {
    // Remove the registered virtual device
    pub fn deregister(&self, device: &str) -> Result<Value> {
        self.device_event("deregisterDevice", device)
    }
    // Request refreshed images for the virtual device
    pub fn rerender_images(&self, device: &str) -> Result<Value> {
        self.device_event("rerenderImages", device)
    }
    // Emit a key-down event for the virtual device
    pub fn key_down(&self, device: &str, position: u8) -> Result<Value> {
        self.press("keyDown", device, position)
    }
    // Emit a key-up event for the virtual device
    pub fn key_up(&self, device: &str, position: u8) -> Result<Value> {
        self.press("keyUp", device, position)
    }
    // Emit an encoder-press event for the virtual device
    pub fn encoder_down(&self, device: &str, position: u8) -> Result<Value> {
        self.press("encoderDown", device, position)
    }
    // Emit an encoder-release event for the virtual device
    pub fn encoder_up(&self, device: &str, position: u8) -> Result<Value> {
        self.press("encoderUp", device, position)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    // Verify scoped provider envelopes
    fn scoped_provider_envelopes() {
        let p = OpenActionDeviceProvider::new("xy").unwrap();
        assert!(p.press("keyDown", "zz-device", 0).is_err());
        assert_eq!(
            p.encoder_change("xy-device", 2, -3).unwrap()["payload"]["ticks"],
            -3
        );
        assert!(OpenActionDeviceProvider::new("é").is_err());
        assert!(p.parse_output(br#"{"event":"setImage","device":"xy-device","controller":null,"position":null,"image":null}"#).is_ok());
        assert!(p
            .parse_output(br#"{"event":"setBrightness","device":"xy-device","brightness":256}"#)
            .is_err());
    }
}
