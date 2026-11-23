//! Plugin SDK for Stream Dock Manager with OpenDeck (OpenAction) export and protocol features shared with VSD Craft and Elgato Stream Deck.
mod atomicfile;
mod completion;
pub mod diagnostics;
pub mod hoststorage;
mod network;
mod percent;
mod pool;
mod protocol;
mod runtime;
mod theme;
pub use completion::*;
pub use hoststorage::*;
pub use network::*;
pub use pool::*;
pub use protocol::*;
pub use runtime::*;
pub use theme::*;
pub type Error = Box<dyn std::error::Error + Send + Sync>;
pub type Result<T = ()> = std::result::Result<T, Error>;
// Construct the SDK error from a caller-facing message
pub(crate) fn error(message: impl Into<String>) -> Error {
    std::io::Error::other(message.into()).into()
}

pub mod authoring;
pub mod httpclient;
pub mod inspectors;
pub mod polling;
pub mod settings;
pub mod storage;
pub mod testkit;

pub mod buildcache;
pub mod connectors;
pub mod credentials;
pub mod process;
pub mod service;

#[cfg(feature = "thumbnail")]
pub mod thumbnail;

pub mod managedstate;

pub mod requests;

#[cfg(feature = "service-websocket")]
pub mod connectedws;

pub mod cache;

pub mod discovery;
pub mod operations;

mod portable_image;
pub use portable_image::{image_data_uri, MAX_PORTABLE_IMAGE_BYTES};

pub mod openaction_events;

pub mod openaction_device;

pub mod permissions;
pub use permissions::{
    NotificationLevel, HTTP_BROKER_PROTOCOL, NOTIFICATIONS_PROTOCOL, PERMISSIONS_PROTOCOL,
    VAULT_REFERENCES_PROTOCOL,
};
pub mod vault_values;
pub use vault_values::{
    VaultLocation, VaultTextValue, VaultValues, MAX_VAULT_TEXT_BYTES, VAULT_VALUES_PROTOCOL,
};
pub use vault_values::{VaultSecretValue, VaultSecrets, VAULT_SECRET_SHARING_PROTOCOL};

pub use vault_values::{VaultWriteReceipt, VAULT_WRITES_PROTOCOL};

pub use vault_values::TEXT_REFERENCES_PROTOCOL;
