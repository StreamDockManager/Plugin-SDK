//! Owned-event-loop plugin SDK. See the repository protocol and compatibility guides
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
