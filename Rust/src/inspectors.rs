//! Local, bundled inspector helpers; no runtime network dependencies
pub const TRANSPORT: &str = include_str!("../web/inspector-sdk.js");
pub const FIELDS: &str = include_str!("../web/inspector-fields.js");

pub const WORKFLOWS: &str = include_str!("../web/inspector-workflows.js");
