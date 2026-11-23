//! Capability-gated host-authorized text reads. No secret sharing or local fallback.
use crate::{Client, RequestOptions, Result, RpcError, UnsupportedFeatureError};
use serde::Deserialize;
use serde_json::json;

pub const VAULT_VALUES_PROTOCOL: &str = "plugin-vault-values-v1";
pub const MAX_VAULT_TEXT_BYTES: usize = 64 << 10;
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct VaultTextValue {
    pub entry_kind: String,
    pub value: String,
    pub revision: String,
}
impl std::fmt::Debug for VaultTextValue {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("VaultTextValue")
            .field("entry_kind", &self.entry_kind)
            .field("value", &"[redacted]")
            .field("revision", &self.revision)
            .finish()
    }
}
fn failure(code: &str, message: &str) -> crate::Error {
    Box::new(RpcError {
        code: code.into(),
        message: message.into(),
    })
}
pub(crate) fn private_error(error: crate::Error) -> crate::Error {
    if error.downcast_ref::<UnsupportedFeatureError>().is_some() {
        return error;
    }
    let code = error
        .downcast_ref::<RpcError>()
        .map(|e| e.code.as_str())
        .filter(|code| {
            matches!(
                *code,
                "needs_setup"
                    | "vault_locked"
                    | "permission_denied"
                    | "not_declared"
                    | "operation_unavailable"
                    | "unsupported"
                    | "invalid_request"
                    | "invalid_response"
                    | "busy"
                    | "quota"
                    | "timeout"
                    | "slot_conflict"
                    | "stale_revision"
                    | "cancelled"
                    | "rate_limited"
                    | "cancelled_before_commit"
                    | "indeterminate"
                    | "activation_required"
                    | "activation_expired"
                    | "no_text"
                    | "unsupported_format"
                    | "invalid_encoding"
                    | "too_large"
                    | "clipboard_changed"
                    | "clipboard_access"
            )
        })
        .unwrap_or("operation_unavailable");
    failure(code, "Manager request did not complete")
}
fn name(value: &str, max: usize) -> Result<()> {
    if value.is_empty()
        || value.len() > max
        || !value
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"_.-".contains(&b))
    {
        return Err(failure("invalid_request", "Invalid Vault reference"));
    }
    Ok(())
}
pub struct VaultLocation {
    pub latitude: f64,
    pub longitude: f64,
    pub revision: String,
}
impl std::fmt::Debug for VaultLocation {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("[Vault location]")
    }
}
pub struct VaultValues {
    client: Client,
    options: RequestOptions,
}
impl VaultValues {
    pub fn new(client: Client) -> Result<Self> {
        if !client.supports(VAULT_VALUES_PROTOCOL) {
            return Err(Box::new(UnsupportedFeatureError(
                VAULT_VALUES_PROTOCOL.into(),
            )));
        }
        Ok(Self {
            client,
            options: RequestOptions::default(),
        })
    }
    pub fn with_request_options(mut self, options: RequestOptions) -> Self {
        self.options = options;
        self
    }
    pub fn read_location(&self, slot: &str, field: &str, action: &str) -> Result<VaultLocation> {
        name(slot, 128)?;
        name(field, 128)?;
        name(action, 256)?;
        let raw = self
            .client
            .request_with_options(
                VAULT_VALUES_PROTOCOL,
                "read-location",
                json!({"slot":slot,"field":field,"action":action}),
                &self.options,
            )
            .map_err(private_error)?;
        #[derive(Deserialize)]
        #[serde(deny_unknown_fields)]
        struct Coordinates {
            latitude: f64,
            longitude: f64,
        }
        #[derive(Deserialize)]
        #[serde(deny_unknown_fields)]
        struct Reply {
            entry_kind: String,
            value: Coordinates,
            revision: String,
        }
        let invalid = || failure("invalid_response", "Invalid Vault location response");
        let reply: Reply = serde_json::from_value(raw).map_err(|_| invalid())?;
        if reply.entry_kind != "location"
            || !(-90.0..=90.0).contains(&reply.value.latitude)
            || !(-180.0..=180.0).contains(&reply.value.longitude)
            || reply
                .revision
                .parse::<u64>()
                .ok()
                .is_none_or(|n| n.to_string() != reply.revision)
        {
            return Err(invalid());
        }
        Ok(VaultLocation {
            latitude: reply.value.latitude,
            longitude: reply.value.longitude,
            revision: reply.revision,
        })
    }
    pub fn read_value(&self, slot: &str, field: &str, action: &str) -> Result<VaultTextValue> {
        name(slot, 128)?;
        name(field, 128)?;
        name(action, 256)?;
        let raw = self
            .client
            .request_with_options(
                VAULT_VALUES_PROTOCOL,
                "read-value",
                json!({"slot":slot,"field":field,"action":action}),
                &self.options,
            )
            .map_err(private_error)?;
        let result: VaultTextValue = serde_json::from_value(raw)
            .map_err(|_| failure("invalid_response", "Invalid Vault text response"))?;
        if result.entry_kind != "text"
            || result.value.len() > MAX_VAULT_TEXT_BYTES
            || result
                .revision
                .parse::<u64>()
                .ok()
                .is_none_or(|n| n.to_string() != result.revision)
        {
            return Err(failure("invalid_response", "Invalid Vault text response"));
        }
        Ok(result)
    }
}

pub const VAULT_SECRET_SHARING_PROTOCOL: &str = "plugin-vault-secret-sharing-v1";
pub struct VaultSecretValue {
    value: String,
    pub entry_kind: String,
    pub revision: String,
}
impl VaultSecretValue {
    pub fn entry_kind(&self) -> &str {
        &self.entry_kind
    }
    /// Explicit disclosure. Never store this value in logs or ordinary settings.
    pub fn expose_value(&self) -> &str {
        &self.value
    }
}
impl std::fmt::Debug for VaultSecretValue {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("VaultSecretValue")
            .field("entry_kind", &self.entry_kind)
            .field("value", &"[redacted]")
            .field("revision", &self.revision)
            .finish()
    }
}
pub struct VaultSecrets {
    client: Client,
    options: RequestOptions,
}
impl VaultSecrets {
    pub fn new(client: Client) -> Result<Self> {
        if !client.supports(VAULT_SECRET_SHARING_PROTOCOL) {
            return Err(Box::new(UnsupportedFeatureError(
                VAULT_SECRET_SHARING_PROTOCOL.into(),
            )));
        }
        Ok(Self {
            client,
            options: RequestOptions::default(),
        })
    }
    pub fn with_request_options(mut self, options: RequestOptions) -> Self {
        self.options = options;
        self
    }
    pub fn read_secret(&self, slot: &str, field: &str, action: &str) -> Result<VaultSecretValue> {
        name(slot, 128)?;
        name(field, 128)?;
        name(action, 256)?;
        let raw = self
            .client
            .request_with_options(
                VAULT_SECRET_SHARING_PROTOCOL,
                "read-secret",
                json!({"slot":slot,"field":field,"action":action}),
                &self.options,
            )
            .map_err(private_error)?;
        let result: VaultTextValue = serde_json::from_value(raw)
            .map_err(|_| failure("invalid_response", "Invalid Vault secret response"))?;
        if !matches!(
            result.entry_kind.as_str(),
            "api-key" | "bearer-token" | "credential"
        ) || result.value.len() > MAX_VAULT_TEXT_BYTES
            || result
                .revision
                .parse::<u64>()
                .ok()
                .is_none_or(|n| n.to_string() != result.revision)
        {
            return Err(failure("invalid_response", "Invalid Vault secret response"));
        }
        Ok(VaultSecretValue {
            entry_kind: result.entry_kind,
            value: result.value,
            revision: result.revision,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn host_errors_are_allowlisted_without_private_messages() {
        for code in [
            "permission_denied",
            "needs_setup",
            "vault_locked",
            "not_declared",
            "operation_unavailable",
            "indeterminate",
            "private-fixture",
        ] {
            let error = private_error(Box::new(RpcError {
                code: code.into(),
                message: "private-fixture".into(),
            }));
            assert!(!error.to_string().contains("private-fixture"));
            assert_eq!(
                error.downcast_ref::<RpcError>().unwrap().code,
                if code == "private-fixture" {
                    "operation_unavailable"
                } else {
                    code
                }
            );
        }
    }
}

pub const VAULT_WRITES_PROTOCOL: &str = "plugin-vault-writes-v1";
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct VaultWriteReceipt {
    pub entry_ref: String,
    pub revision: String,
}
fn positive_revision(s: &str) -> bool {
    s.parse::<u64>().is_ok_and(|r| r > 0 && r.to_string() == s)
}
impl Client {
    /// Creates an owner-private slot, or rotates it using the expected revision.
    #[expect(
        clippy::too_many_arguments,
        reason = "Preserve the public Vault write API"
    )]
    pub fn write_vault_secret(
        &self,
        slot: &str,
        field: &str,
        action: &str,
        kind: &str,
        value: &str,
        expected_revision: Option<&str>,
        options: &RequestOptions,
    ) -> Result<VaultWriteReceipt> {
        name(slot, 128)?;
        name(field, 128)?;
        name(action, 256)?;
        if !matches!(kind, "credential" | "api-key" | "bearer-token")
            || value.is_empty()
            || value.len() > MAX_VAULT_TEXT_BYTES
            || expected_revision.is_some_and(|s| !positive_revision(s))
        {
            return Err(failure("invalid_request", "Invalid Vault write"));
        }
        let mut payload = json!({"operation":"create","slot":slot,"field":field,"action":action,"entry_kind":kind,"value":value});
        if let Some(revision) = expected_revision {
            payload["operation"] = json!("rotate");
            payload["expected_revision"] = json!(revision);
        }
        let raw = self
            .request_with_options(VAULT_WRITES_PROTOCOL, "write-secret", payload, options)
            .map_err(private_error)?;
        let invalid = || failure("invalid_response", "Invalid Vault write receipt");
        let result: VaultWriteReceipt = serde_json::from_value(raw).map_err(|_| invalid())?;
        let uuid = |s: &str| uuid::Uuid::parse_str(s).is_ok_and(|v| v.to_string() == s);
        if !result
            .entry_ref
            .split_once('.')
            .is_some_and(|(a, b)| uuid(a) && uuid(b))
            || !positive_revision(&result.revision)
        {
            return Err(invalid());
        }
        Ok(result)
    }
}

pub const TEXT_REFERENCES_PROTOCOL: &str = "plugin-text-references-v1";

impl Client {
    /// Resolve one transient string in a permission-checked Manager action scope
    pub fn format_text(&self, action: &str, text: &str) -> Result<String> {
        self.format_text_with_options(action, text, &RequestOptions::default())
    }

    /// Resolve text with explicit cancellation and timeout options
    pub fn format_text_with_options(
        &self,
        action: &str,
        text: &str,
        options: &RequestOptions,
    ) -> Result<String> {
        if text.len() > 64 << 10 {
            return Err(failure("invalid_request", "Invalid text to format"));
        }
        if !text
            .split_once("${")
            .is_some_and(|(_, tail)| tail.contains('}'))
        {
            return Ok(text.into());
        }
        if !self.supports(TEXT_REFERENCES_PROTOCOL) {
            return Err(Box::new(UnsupportedFeatureError(
                TEXT_REFERENCES_PROTOCOL.into(),
            )));
        }
        name(action, 256)?;
        let raw = self
            .request_with_options(
                TEXT_REFERENCES_PROTOCOL,
                "format-text",
                json!({"action":action,"text":text}),
                options,
            )
            .map_err(private_error)?;
        #[derive(Deserialize)]
        #[serde(deny_unknown_fields)]
        struct Reply {
            text: String,
        }
        let invalid = || failure("invalid_response", "Invalid formatted text response");
        let reply: Reply = serde_json::from_value(raw).map_err(|_| invalid())?;
        if reply.text.len() > 256 << 10 {
            return Err(invalid());
        }
        Ok(reply.text)
    }
}
