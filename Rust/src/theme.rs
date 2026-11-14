use crate::{error, Client, Result, UnsupportedFeatureError};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

pub const THEME_PROTOCOL: &str = "plugin-theme-v1";
pub const THEME_EVENT: &str = "didReceivePluginTheme";

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct PluginTheme {
    pub version: u32,
    pub resolved: String,
    pub revision: u64,
    pub theme: ThemeColors,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ThemeColors {
    pub surface: String,
    pub ink: String,
    pub muted: String,
    pub accent: String,
    pub meter: String,
    pub border: String,
    pub wallpaper: ThemeWallpaper,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(untagged)]
pub enum ThemeWallpaper {
    Svg(String),
    Media {
        kind: String,
        asset_id: String,
        format: String,
        animated: bool,
    },
}

// Decode the supplied data under its validation policy
fn decode(value: Value) -> Result<PluginTheme> {
    let value: PluginTheme = serde_json::from_value(value)?;
    let theme = &value.theme;
    let colors = [
        &theme.surface,
        &theme.ink,
        &theme.muted,
        &theme.accent,
        &theme.meter,
        &theme.border,
    ];
    let wallpaper = match &theme.wallpaper {
        ThemeWallpaper::Svg(svg) => !svg.is_empty() && svg.len() <= 64 * 1024,
        ThemeWallpaper::Media {
            kind,
            asset_id,
            format,
            ..
        } => kind == "media" && !asset_id.is_empty() && !format.is_empty(),
    };
    if value.version != 1
        || value.revision > (1 << 53) - 1
        || !matches!(value.resolved.as_str(), "light" | "dark")
        || !wallpaper
        || colors.iter().any(|color| {
            color.len() != 7
                || !color.starts_with('#')
                || !color.as_bytes()[1..].iter().all(u8::is_ascii_hexdigit)
        })
    {
        return Err(error("invalid plugin theme"));
    }
    Ok(value)
}

impl Client {
    // Check whether plugin-theme RPC was negotiated
    pub fn supports_theme_rpc(&self) -> bool {
        self.supports(THEME_PROTOCOL) && self.host().raw["streamDockManager"]["theme_rpc"] == true
    }

    // Request the current theme through the supported host capability
    pub fn get_plugin_theme(&self) -> Result<PluginTheme> {
        if !self.supports_theme_rpc() {
            return Err(Box::new(UnsupportedFeatureError(THEME_PROTOCOL.into())));
        }
        let value = decode(self.request(THEME_PROTOCOL, "get", json!({}))?)?;
        self.remember_plugin_theme(value);
        self.current_plugin_theme()
            .ok_or_else(|| error("plugin theme unavailable"))
    }

    // Read the most recently observed plugin theme
    pub fn current_plugin_theme(&self) -> Option<PluginTheme> {
        self.inner.plugin_theme.lock().unwrap().clone()
    }

    // Store the validated theme observation
    fn remember_plugin_theme(&self, value: PluginTheme) -> bool {
        let mut current = self.inner.plugin_theme.lock().unwrap();
        if current
            .as_ref()
            .is_some_and(|current| current.revision >= value.revision)
        {
            return false;
        }
        *current = Some(value);
        true
    }

    // Consume a matching theme notification
    pub(crate) fn observe_plugin_theme(&self, payload: &Value) -> bool {
        self.supports_theme_rpc()
            && decode(payload.clone()).is_ok_and(|value| self.remember_plugin_theme(value))
    }
}
