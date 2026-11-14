//! Explicit credentials and safe diagnostics. Environment access happens only on a call
use crate::percent::encoded;
use crate::{error, Result};
use serde_json::Value;
// Resolve a literal or environment-backed credential
pub fn secret(settings: &Value, key: &str) -> Result<String> {
    secret_with(settings, key, |name| std::env::var(name).ok())
}
// Resolve a credential with the supplied environment lookup
pub fn secret_with(
    settings: &Value,
    key: &str,
    lookup: impl Fn(&str) -> Option<String>,
) -> Result<String> {
    let value = settings[key].as_str().unwrap_or("");
    if value.starts_with("${") && value.ends_with('}') {
        let name = &value[2..value.len() - 1];
        if !name.is_empty()
            && name
                .chars()
                .enumerate()
                .all(|(i, c)| c == '_' || c.is_ascii_alphabetic() || (i > 0 && c.is_ascii_digit()))
        {
            return lookup(name)
                .filter(|v| !v.is_empty())
                .ok_or_else(|| error(format!("{key} environment reference is unset")));
        }
    }
    if value.contains("${") {
        return Err(error(format!(
            "{key} requires a whole-value environment reference"
        )));
    }
    Ok(value.into())
}
// Expose a credential-safe description of a failure
pub fn safe_error(failure: crate::Error, settings: &Value, keys: &[&str]) -> crate::Error {
    if let Some(e) = failure.downcast_ref::<crate::service::HTTPError>() {
        return Box::new(e.clone());
    }
    let mut text = failure.to_string();
    if let Some(fields) = settings.as_object() {
        for (key, value) in fields {
            let low = key.to_lowercase();
            if !keys.contains(&key.as_str())
                && ![
                    "password",
                    "secret",
                    "token",
                    "api_key",
                    "api-key",
                    "webhook",
                    "authorization",
                    "cookie",
                ]
                .iter()
                .any(|part| low.contains(part))
            {
                continue;
            }
            if let Some(raw) = value.as_str() {
                for value in [raw.to_string(), secret(settings, key).unwrap_or_default()] {
                    if !value.is_empty() {
                        text = text
                            .replace(&value, "[redacted]")
                            .replace(&encoded(&value), "[redacted]")
                            .replace(&encoded(&value).replace("%20", "+"), "[redacted]");
                    }
                }
            }
        }
    }
    for scheme in ["https://", "http://"] {
        while let Some(start) = text.find(scheme) {
            let len = text[start..]
                .find(|c: char| c.is_whitespace() || "\"<>".contains(c))
                .unwrap_or(text.len() - start);
            text.replace_range(start..start + len, "[service URL]");
        }
    }
    error(text)
}
