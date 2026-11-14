//! Opt-in OpenAction host notifications. These functions never open UI or URLs
use crate::{error, Event, Result};

pub const OPENACTION_SHOW_SETTINGS_INTERFACE: &str = "showSettingsInterface";
pub const OPENACTION_DID_RECEIVE_DEEP_LINK: &str = "didReceiveDeepLink";

/// Recognize the payload-free host request. Advertise HasSettingsInterface only
/// when your event handler implements the requested UI
pub fn openaction_settings_requested(event: &Event) -> bool {
    event.event == OPENACTION_SHOW_SETTINGS_INTERFACE
}

/// Return the untrusted opaque URL without decoding or executing it
pub fn openaction_deep_link(event: &Event) -> Result<Option<&str>> {
    if event.event != OPENACTION_DID_RECEIVE_DEEP_LINK {
        return Ok(None);
    }
    match event.payload.get("url").and_then(|value| value.as_str()) {
        Some(url) if !url.is_empty() => Ok(Some(url)),
        _ => Err(error(
            "OpenAction deep link requires a nonempty payload.url string",
        )),
    }
}
