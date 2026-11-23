//! Manager service requests use the authenticated host socket and the caller's activation.
use crate::{Client, RequestOptions, Result};
use serde::Deserialize;
use serde_json::{json, Value};
pub const PERMISSIONS_PROTOCOL: &str = "plugin-permissions-v1";
pub const HTTP_BROKER_PROTOCOL: &str = "plugin-http-broker-v1";

#[derive(Clone, Deserialize)]
pub struct PermissionStatus {
    pub granted: bool,
    pub available: bool,
    pub revision: String,
}
#[derive(Clone, Deserialize)]
pub struct ActionPermissionStatus {
    pub action: String,
    pub scope: Value,
    pub granted: bool,
    pub available: bool,
}
#[derive(Clone, Deserialize)]
pub struct PermissionList {
    pub revision: String,
    pub scopes: Vec<ActionPermissionStatus>,
}
#[derive(Clone, Deserialize)]
pub struct PermissionReview {
    pub review_id: String,
    pub revision: String,
    pub state: String,
}
#[derive(Clone, Deserialize)]
pub struct RecipeResult {
    pub result_handle: String,
    pub state: String,
}

impl Client {
    fn permission_request<T: serde::de::DeserializeOwned>(
        &self,
        protocol: &str,
        method: &str,
        payload: Value,
        options: &RequestOptions,
    ) -> Result<T> {
        let reply = self
            .request_with_options(protocol, method, payload, options)
            .map_err(crate::vault_values::private_error)?;
        serde_json::from_value(reply).map_err(|_| {
            Box::new(crate::RpcError {
                code: "invalid_response".into(),
                message: "Invalid Manager service response".into(),
            }) as crate::Error
        })
    }

    pub fn get_permissions(&self, options: &RequestOptions) -> Result<PermissionList> {
        self.permission_request(PERMISSIONS_PROTOCOL, "get-permissions", json!({}), options)
    }
    pub fn has_permission(
        &self,
        action: &str,
        scope: &Value,
        options: &RequestOptions,
    ) -> Result<PermissionStatus> {
        self.permission_request(
            PERMISSIONS_PROTOCOL,
            "has-permission",
            json!({"action":action,"scope":scope}),
            options,
        )
    }
    pub fn request_permissions(
        &self,
        activation: &str,
        scopes: &[Value],
        options: &RequestOptions,
    ) -> Result<PermissionReview> {
        self.permission_request(
            PERMISSIONS_PROTOCOL,
            "request-permissions",
            json!({"activation_token":activation,"scopes":scopes}),
            options,
        )
    }
    pub fn send_example_webhook(
        &self,
        activation: &str,
        certificate: &str,
        options: &RequestOptions,
    ) -> Result<RecipeResult> {
        self.permission_request(
            HTTP_BROKER_PROTOCOL,
            "manager-webhook",
            json!({"activation_token":activation,"certificate":certificate}),
            options,
        )
    }
    pub fn send_local_webhook(
        &self,
        activation: &str,
        origin: &str,
        options: &RequestOptions,
    ) -> Result<RecipeResult> {
        self.permission_request(
            HTTP_BROKER_PROTOCOL,
            "manager-local-webhook",
            json!({"activation_token":activation,"origin":origin}),
            options,
        )
    }

    pub fn summarize_clipboard(
        &self,
        activation: &str,
        options: &RequestOptions,
    ) -> Result<RecipeResult> {
        self.permission_request(
            HTTP_BROKER_PROTOCOL,
            "clipboard-summary",
            json!({"activation_token":activation}),
            options,
        )
    }

    pub fn read_clipboard_text(
        &self,
        activation: &str,
        options: &RequestOptions,
    ) -> Result<ClipboardText> {
        self.permission_request(
            PERMISSIONS_PROTOCOL,
            "read-clipboard-text",
            json!({"activation_token":activation}),
            options,
        )
    }
    pub fn show_text_result(
        &self,
        activation: &str,
        text: &str,
        options: &RequestOptions,
    ) -> Result<RecipeResult> {
        self.permission_request(
            PERMISSIONS_PROTOCOL,
            "show-text-result",
            json!({"activation_token":activation,"text":text}),
            options,
        )
    }
    pub fn notify_completion(
        &self,
        activation: &str,
        options: &RequestOptions,
    ) -> Result<CompletionStatus> {
        self.permission_request(
            PERMISSIONS_PROTOCOL,
            "notify-completion",
            json!({"activation_token":activation}),
            options,
        )
    }
}
#[derive(Deserialize)]
pub struct ClipboardText {
    pub text: String,
    pub revision: String,
}
#[derive(Deserialize)]
pub struct CompletionStatus {
    pub state: String,
}

pub const VAULT_REFERENCES_PROTOCOL: &str = "plugin-vault-references-v1";
#[derive(Deserialize)]
pub struct VaultEntryName {
    pub id: String,
    pub name: String,
}
#[derive(Deserialize)]
pub struct VaultEntryPage {
    pub entries: Vec<VaultEntryName>,
    pub next: Option<usize>,
}
impl Client {
    pub fn list_vault_entries(
        &self,
        action: &str,
        scope: &Value,
        start: usize,
        options: &RequestOptions,
    ) -> Result<VaultEntryPage> {
        self.permission_request(
            VAULT_REFERENCES_PROTOCOL,
            "list-vault-entries",
            json!({"action":action,"scope":scope,"start":start}),
            options,
        )
    }
    pub fn select_vault_entry(
        &self,
        activation: &str,
        action: &str,
        scope: &Value,
        options: &RequestOptions,
    ) -> Result<PermissionReview> {
        self.permission_request(
            VAULT_REFERENCES_PROTOCOL,
            "select-vault-entry",
            json!({"action":action,"scope":scope,"activation_token":activation}),
            options,
        )
    }
    pub fn run_vault_recipe(
        &self,
        activation: &str,
        scope: &Value,
        entry_ref: &str,
        options: &RequestOptions,
    ) -> Result<RecipeResult> {
        let Some(method @ "clipboard-summary") = scope["recipe"].as_str() else {
            return Err(Box::new(crate::RpcError {
                code: "invalid_request".into(),
                message: "Unknown Vault recipe".into(),
            }));
        };
        self.permission_request(
            HTTP_BROKER_PROTOCOL,
            method,
            json!({"scope":scope,"entry_ref":entry_ref,"activation_token":activation}),
            options,
        )
    }
}

/// Manager emits terminal events to the originating connection and changes to the affected plugin.
#[derive(Deserialize)]
#[serde(tag = "event")]
pub enum PermissionNotice {
    #[serde(rename = "streamDockManagerPermissionRequestResult")]
    RequestResult {
        request_id: String,
        review_id: String,
        revision: String,
        outcome: String,
    },
    #[serde(rename = "streamDockManagerPermissionsChanged")]
    Changed {
        action: String,
        revision: String,
        scopes: Vec<Value>,
    },
    #[serde(rename = "streamDockManagerVaultSelectionResult")]
    Selection {
        request_id: String,
        review_id: String,
        outcome: String,
        entry: Option<VaultEntryName>,
    },
}
impl crate::Event {
    pub fn permission_notice(&self) -> Option<PermissionNotice> {
        match self.event.as_str() {
            "streamDockManagerPermissionRequestResult"
            | "streamDockManagerPermissionsChanged"
            | "streamDockManagerVaultSelectionResult" => {
                serde_json::from_value(serde_json::to_value(self).ok()?).ok()
            }
            _ => None,
        }
    }
}

/// Manager feature flag for action-scoped plain-text notifications
pub const NOTIFICATIONS_PROTOCOL: &str = "plugin-notifications-v1";

#[derive(Clone, Copy, Debug, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum NotificationLevel {
    Info,
    Success,
    Warning,
    Error,
}

impl Client {
    /// Send a plain-text notice under the reviewed action scope
    pub fn create_notification(
        &self,
        action: &str,
        level: NotificationLevel,
        message: &str,
        options: &RequestOptions,
    ) -> Result<()> {
        if !self.supports(NOTIFICATIONS_PROTOCOL) {
            return Err(Box::new(crate::UnsupportedFeatureError(
                NOTIFICATIONS_PROTOCOL.into(),
            )));
        }
        if action.is_empty()
            || action.len() > 256
            || !action
                .bytes()
                .all(|byte| byte.is_ascii_alphanumeric() || b"_.-".contains(&byte))
            || message.trim().is_empty()
        {
            return Err(Box::new(crate::RpcError {
                code: "invalid_request".into(),
                message: "Invalid notification action or message".into(),
            }));
        }
        if message.len() > 512 {
            return Err(Box::new(crate::RpcError {
                code: "too_large".into(),
                message: "Notification message exceeds 512 UTF-8 bytes".into(),
            }));
        }
        let receipt: CompletionStatus = self.permission_request(
            PERMISSIONS_PROTOCOL,
            "create-notification",
            json!({"action":action,"level":level,"message":message}),
            options,
        )?;
        if receipt.state != "created" {
            return Err(Box::new(crate::RpcError {
                code: "invalid_response".into(),
                message: "Invalid notification receipt".into(),
            }));
        }
        Ok(())
    }
}
