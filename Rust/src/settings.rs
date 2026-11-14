//! Declarative inspector fields and runtime validation, with unknown-key retention
use crate::{error, Result};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Field {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub path_picker: String,
    pub key: String,
    pub label: String,
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(
        skip_serializing_if = "Option::is_none",
        deserialize_with = "present_default"
    )]
    pub default: Option<Value>,
    pub options: Vec<String>,
    #[serde(skip_serializing_if = "std::collections::BTreeMap::is_empty")]
    pub option_labels: std::collections::BTreeMap<String, String>,
    pub min: Option<f64>,
    pub max: Option<f64>,
    pub required: bool,
    pub sensitive: bool,
}
// Choose the schema default when no value is supplied
fn present_default<'de, D: serde::Deserializer<'de>>(
    decoder: D,
) -> std::result::Result<Option<Value>, D::Error> {
    Value::deserialize(decoder).map(Some)
}

// Merge supplied settings while retaining unknown keys
pub fn merge(global: &Value, action: &Value) -> Value {
    let mut out = global.as_object().cloned().unwrap_or_default();
    if let Some(values) = action.as_object() {
        out.extend(values.clone())
    };
    Value::Object(out)
}
// Reject values outside the supported contract for settings validation
pub fn validate(fields: &[Field], input: &Value) -> Result<Value> {
    if fields.len() > 256 {
        return Err(error("too many settings fields"));
    }
    let mut out = input
        .as_object()
        .cloned()
        .ok_or_else(|| error("settings must be an object"))?;
    let mut seen = std::collections::HashSet::new();
    for field in fields {
        if !field.path_picker.is_empty()
            && (field.kind != "path"
                || !["file", "folder", "save"].contains(&field.path_picker.as_str()))
        {
            return Err(error("invalid path picker"));
        }
        if field.sensitive && ["number", "checkbox", "pairs"].contains(&field.kind.as_str()) {
            return Err(error("sensitive requires a string field"));
        }
        if field.key.is_empty() || !seen.insert(&field.key) {
            return Err(error("empty or duplicate settings field"));
        }
        if field.min.is_some_and(|v| !v.is_finite())
            || field.max.is_some_and(|v| !v.is_finite())
            || field.min.zip(field.max).is_some_and(|(a, b)| a > b)
        {
            return Err(error(format!("{}: invalid bounds", field.key)));
        }
        if ![
            "text", "textarea", "path", "device", "select", "checkbox", "number", "pairs",
        ]
        .contains(&field.kind.as_str())
        {
            return Err(error(format!("{}: unknown field type", field.key)));
        }
        if !out.contains_key(&field.key) {
            if let Some(value) = &field.default {
                out.insert(field.key.clone(), value.clone());
            } else if field.required {
                return Err(error(format!("{}: required", field.key)));
            } else {
                continue;
            }
        }
        let value = &out[&field.key];
        let valid = match field.kind.as_str() {
            "text" | "textarea" | "path" | "device" => value
                .as_str()
                .is_some_and(|v| !field.required || !v.is_empty()),
            "select" => value.as_str().is_some_and(|v| {
                field.options.iter().any(|x| x == v) && (!field.required || !v.is_empty())
            }),
            "checkbox" => value.is_boolean(),
            "number" => value.as_f64().is_some_and(|v| {
                v.is_finite()
                    && field.min.is_none_or(|lo| v >= lo)
                    && field.max.is_none_or(|hi| v <= hi)
            }),
            "pairs" => value
                .as_object()
                .is_some_and(|v| v.values().all(Value::is_string)),
            _ => return Err(error(format!("{}: unknown field type", field.key))),
        };
        if !valid {
            return Err(error(format!(
                "{}: invalid {} value",
                field.key, field.kind
            )));
        }
    }
    Ok(Value::Object(out))
}
// Validate an object-valued setting
pub fn object() -> Value {
    Value::Object(Map::new())
}

// Validate a string-valued setting
pub fn string(settings: &Value, key: &str, fallback: &str) -> String {
    settings[key]
        .as_str()
        .filter(|v| !v.is_empty())
        .unwrap_or(fallback)
        .into()
}
// Validate a numeric setting
pub fn number(settings: &Value, key: &str, fallback: f64) -> f64 {
    settings[key]
        .as_f64()
        .or_else(|| settings[key].as_str().and_then(|x| x.trim().parse().ok()))
        .filter(|v| v.is_finite())
        .unwrap_or(fallback)
}
// Validate a boolean-valued setting
pub fn boolean(settings: &Value, key: &str) -> bool {
    settings[key].as_bool().unwrap_or(false)
}

#[cfg(test)]
mod label_tests {
    use super::*;

    #[test]
    // Verify named selects round trip labels without changing stored ids
    fn named_selects_round_trip_labels_without_changing_stored_ids() {
        let field: Field = serde_json::from_value(serde_json::json!({
            "key":"skill", "type":"select", "options":["0","1"],
            "option_labels":{"0":"Overall","1":"Attack"}
        }))
        .unwrap();
        let saved = validate(
            std::slice::from_ref(&field),
            &serde_json::json!({"skill":"1","unknown":true}),
        )
        .unwrap();
        assert_eq!(saved["skill"], "1");
        assert_eq!(saved["unknown"], true);
        assert_eq!(
            serde_json::to_value(field).unwrap()["option_labels"]["1"],
            "Attack"
        );
    }
}

/// Internal connector projection; all returned JSON values are independent clones
pub(crate) fn project(global: &Value, action: &Value, shared_keys: &[String]) -> Value {
    let mut out = merge(global, action);
    let fields = out.as_object_mut().expect("merge returns an object");
    for key in shared_keys {
        if let Some(value) = global.get(key) {
            fields.insert(key.clone(), value.clone());
        } else {
            fields.remove(key);
        }
    }
    out
}

#[cfg(test)]
mod projection_tests {
    #[test]
    // Verify shared keys and clones
    fn shared_keys_and_clones() {
        let global = serde_json::json!({"shared":{"v":[1]},"global":[2]});
        let action = serde_json::json!({"shared":{"v":[3]},"missing":true,"local":[4]});
        let mut value = super::project(&global, &action, &["shared".into(), "missing".into()]);
        assert!(value.get("missing").is_none());
        assert_eq!(value["shared"], global["shared"]);
        value["shared"]["v"][0] = serde_json::json!(9);
        value["global"][0] = serde_json::json!(9);
        value["local"][0] = serde_json::json!(9);
        assert_eq!(global, serde_json::json!({"shared":{"v":[1]},"global":[2]}));
        assert_eq!(action["local"], serde_json::json!([4]));
        assert_eq!(
            super::project(&global, &action, &[])["shared"],
            action["shared"]
        );
    }
}
