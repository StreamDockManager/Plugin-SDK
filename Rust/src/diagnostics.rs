//! Bounded, session-owned network diagnostics, independent of HTTP dependencies
use crate::percent::encoded;
use base64::Engine;
use serde_json::{json, Value};
use std::{
    collections::BTreeMap,
    sync::{Arc, Mutex, Weak},
    time::Instant,
};

const BODY: usize = 64 << 10;
const HEADER: usize = 8 << 10;
type Sink = Arc<dyn Fn(Value, bool) + Send + Sync>;
type Redactor = Arc<dyn Fn(&str) -> String + Send + Sync>;
struct Configuration {
    sink: Sink,
    redact: Redactor,
}
#[derive(Clone, Default)]
pub struct NetworkDiagnostics {
    active: Arc<Mutex<Option<Arc<Configuration>>>>,
    sequence: Arc<std::sync::atomic::AtomicU64>,
}
impl NetworkDiagnostics {
    // Apply the requested execution or diagnostics configuration
    pub fn configure(
        &self,
        enabled: bool,
        sink: impl Fn(Value, bool) + Send + Sync + 'static,
        redact: impl Fn(&str) -> String + Send + Sync + 'static,
    ) {
        let mut active = self.active.lock().unwrap();
        if !enabled {
            *active = None;
        } else if active.is_none() {
            *active = Some(Arc::new(Configuration {
                sink: Arc::new(sink),
                redact: Arc::new(redact),
            }));
        }
    }
    // Record diagnostics for an I/O operation
    pub fn io(&self, operation: &str, bytes: usize) {
        let owner = self.active.lock().unwrap().clone();
        if let Some(owner) = owner {
            (owner.sink)(
                json!({"message":format!("Connection {operation} · {bytes} bytes")}),
                false,
            );
        }
    }
    // Start the requested observation or operation for redacted network diagnostics
    pub fn start(
        &self,
        method: &str,
        url: &str,
        headers: &BTreeMap<String, String>,
    ) -> Option<NetworkTrace> {
        let owner = self.active.lock().unwrap().clone()?;
        let mut captured = BTreeMap::new();
        let mut used = 0;
        let mut overflow = false;
        for (name, value) in headers {
            if captured.len() >= 128 || name.len() > 256 || used + name.len() + value.len() > BODY {
                overflow = true;
                continue;
            }
            used += name.len() + value.len();
            captured.insert(name.clone(), vec![value.clone()]);
        }
        Some(NetworkTrace {
            headers_overflow: overflow,
            owner,
            active: Arc::downgrade(&self.active),
            id: self
                .sequence
                .fetch_add(1, std::sync::atomic::Ordering::Relaxed)
                + 1,
            method: method.chars().filter(|c| !c.is_control()).collect(),
            url: endpoint(url),
            started: Instant::now(),
            request: Capture::new(content_type(headers)),
            response: Capture::default(),
            request_headers: captured,
            response_headers: BTreeMap::new(),
            finished: false,
        })
    }
}
// Select the response content type for diagnostic interpretation
fn content_type(headers: &BTreeMap<String, String>) -> String {
    headers
        .iter()
        .find(|(k, _)| k.eq_ignore_ascii_case("content-type"))
        .map(|(_, v)| v.clone())
        .unwrap_or_default()
}
// Normalize the input before matching diagnostic policy
fn normalized(key: &str) -> String {
    key.chars()
        .filter(char::is_ascii_alphanumeric)
        .flat_map(char::to_lowercase)
        .collect()
}
// Identify headers whose values require redaction
fn sensitive_header(key: &str) -> bool {
    let key = normalized(key);
    if ["authority", "accesscontrolallowcredentials"].contains(&key.as_str()) {
        return false;
    }
    [
        "auth",
        "cookie",
        "token",
        "secret",
        "password",
        "credential",
        "signature",
        "apikey",
        "csrf",
        "xsrf",
        "hmac",
        "bearer",
        "sessionid",
    ]
    .iter()
    .any(|part| key.contains(part))
        || key.ends_with("key")
        || key.ends_with("session")
}
// Identify fields that must not appear in diagnostics
fn sensitive(key: &str) -> bool {
    let key = normalized(key);
    [
        "password",
        "secret",
        "token",
        "apikey",
        "authorization",
        "cookie",
        "webhook",
        "credential",
        "signature",
    ]
    .iter()
    .any(|part| key.contains(part))
}
// Decode an encoded diagnostic field before redaction
fn decoded(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = Vec::new();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            if let (Some(a), Some(b)) = (
                (bytes[i + 1] as char).to_digit(16),
                (bytes[i + 2] as char).to_digit(16),
            ) {
                out.push((a * 16 + b) as u8);
                i += 3;
                continue;
            }
        }
        out.push(if bytes[i] == b'+' { b' ' } else { bytes[i] });
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}
// Return a redacted diagnostic representation of a header
fn header_value(name: &str, value: &str) -> String {
    let key = normalized(name);
    if key.ends_with("url")
        || key.ends_with("uri")
        || ["location", "contentlocation", "referer", "destination"].contains(&key.as_str())
        || value.starts_with(['/', '?'])
    {
        return endpoint(value);
    }
    let mut safe = String::new();
    let mut rest = value;
    while let Some(marker) = rest.find("://") {
        let start = rest[..marker]
            .char_indices()
            .rev()
            .find(|(_, c)| !c.is_ascii_alphanumeric() && !"+.-".contains(*c))
            .map(|(i, c)| i + c.len_utf8())
            .unwrap_or(0);
        let end = rest[marker + 3..]
            .find(|c: char| c.is_whitespace() || "\"<>".contains(c))
            .map(|i| marker + 3 + i)
            .unwrap_or(rest.len());
        safe.push_str(&rest[..start]);
        safe.push_str(&endpoint(&rest[start..end]));
        rest = &rest[end..];
    }
    safe.push_str(rest);
    safe
}
// Remove sensitive URL components from diagnostic output
fn endpoint(raw: &str) -> String {
    let raw = raw.split('#').next().unwrap_or("");
    let (base, query) = raw.split_once('?').unwrap_or((raw, ""));
    let base = if let Some((scheme, rest)) = base.split_once("://") {
        let (authority, path) = rest.split_once('/').unwrap_or((rest, ""));
        format!(
            "{scheme}://{}/{}",
            authority.rsplit('@').next().unwrap_or(""),
            path
        )
    } else {
        base.to_string()
    };
    if query.is_empty() {
        return base;
    }
    format!(
        "{base}?{}",
        query
            .split('&')
            .map(|pair| format!("{}=%5Bredacted%5D", pair.split('=').next().unwrap_or("")))
            .collect::<Vec<_>>()
            .join("&")
    )
}
#[derive(Default)]
pub struct Capture {
    data: Vec<u8>,
    bytes: usize,
    pub complete: bool,
    truncated: bool,
    content_type: String,
}
impl Capture {
    // Initialize the instance and its owned state for redacted network diagnostics
    fn new(content_type: String) -> Self {
        let content_type = if content_type.len() > 256 {
            "[content type omitted]".into()
        } else {
            content_type
        };
        Self {
            content_type,
            ..Self::default()
        }
    }
    // Write data through the owned transport or state backend for redacted network diagnostics
    pub fn write(&mut self, bytes: &[u8]) {
        self.bytes = self.bytes.saturating_add(bytes.len());
        if self.bytes > BODY {
            self.truncated = true;
            self.data.clear();
        } else if !self.truncated {
            self.data.extend_from_slice(bytes);
        }
    }
    // Copy the current observable state for redacted network diagnostics
    fn snapshot(&self, redact: &impl Fn(&str) -> String) -> Value {
        let binary = std::str::from_utf8(&self.data).is_err()
            || self
                .data
                .iter()
                .any(|b| *b < 32 && !b"\r\n\t".contains(b) || *b == 127);
        let mut text = String::from_utf8_lossy(&self.data).into_owned();
        let original = text.clone();
        // Redact diagnostic text before it leaves the process
        fn clean(value: &mut Value, redact: &impl Fn(&str) -> String) {
            match value {
                Value::Object(map) => {
                    for (key, item) in map {
                        if sensitive(key) {
                            *item = json!("[redacted]");
                        } else {
                            clean(item, redact);
                        }
                    }
                }
                Value::Array(items) => {
                    for item in items {
                        clean(item, redact);
                    }
                }
                Value::String(text) => *text = redact(text),
                _ => {}
            }
        }
        if let Ok(mut value) = serde_json::from_str::<Value>(&text) {
            clean(&mut value, redact);
            text = value.to_string();
        } else if !text.is_empty()
            && (text.trim_start().starts_with(['[', '{'])
                || self.content_type.to_lowercase().contains("json"))
        {
            return json!({"text":"","encoding":"utf8","content_type":redact(&self.content_type),"bytes":self.bytes,"complete":self.complete,"truncated":true});
        } else if self
            .content_type
            .to_lowercase()
            .contains("application/x-www-form-urlencoded")
        {
            text = text
                .split('&')
                .map(|pair| {
                    let (key, value) = pair.split_once('=').unwrap_or((pair, ""));
                    format!(
                        "{key}={}",
                        if sensitive(&decoded(key)) {
                            "%5Bredacted%5D"
                        } else {
                            value
                        }
                    )
                })
                .collect::<Vec<_>>()
                .join("&");
        }
        text = redact(&text);
        let raw = if binary && text == original {
            self.data.clone()
        } else {
            text.into_bytes()
        };
        let truncated = self.truncated || raw.len() > BODY;
        let raw = if truncated { &[][..] } else { raw.as_slice() };
        json!({"text":if binary { base64::engine::general_purpose::STANDARD.encode(raw) } else { String::from_utf8_lossy(raw).into_owned() },"encoding":if binary {"base64"}else{"utf8"},"content_type":redact(&self.content_type).chars().take(256).collect::<String>(),"bytes":self.bytes,"complete":self.complete,"truncated":truncated})
    }
}
pub struct NetworkTrace {
    owner: Arc<Configuration>,
    active: Weak<Mutex<Option<Arc<Configuration>>>>,
    id: u64,
    method: String,
    url: String,
    started: Instant,
    pub request: Capture,
    pub response: Capture,
    request_headers: BTreeMap<String, Vec<String>>,
    response_headers: BTreeMap<String, Vec<String>>,
    finished: bool,
    headers_overflow: bool,
}
impl NetworkTrace {
    // Account for received response bytes
    pub fn received(&mut self, headers: BTreeMap<String, Vec<String>>) {
        self.response = Capture::new(
            headers
                .iter()
                .find(|(k, _)| k.eq_ignore_ascii_case("content-type"))
                .and_then(|(_, v)| v.first())
                .cloned()
                .unwrap_or_default(),
        );
        let mut captured = BTreeMap::new();
        let mut used = 0;
        for (name, values) in headers {
            let size = name.len() + values.iter().map(String::len).sum::<usize>();
            if captured.len() >= 128 || name.len() > 256 || used + size > BODY {
                self.headers_overflow = true;
                continue;
            }
            used += size;
            captured.insert(name, values);
        }
        self.response_headers = captured;
    }
    // Complete the operation exactly once for redacted network diagnostics
    pub fn finish(&mut self, status: u16, failed: bool) {
        if self.finished {
            return;
        }
        self.finished = true;
        let Some(active) = self.active.upgrade() else {
            return;
        };
        if !active
            .lock()
            .unwrap()
            .as_ref()
            .is_some_and(|owner| Arc::ptr_eq(owner, &self.owner))
        {
            return;
        }
        let mut secrets = Vec::<String>::new();
        let mut used = 0;
        let mut overflow = self.headers_overflow;
        let mut remember = |value: &str| {
            if value.is_empty() || secrets.iter().any(|v| v == value) {
                return;
            }
            used += value.len();
            if secrets.len() >= 256 || used > BODY {
                overflow = true;
            } else {
                secrets.push(value.into());
            }
        };
        for headers in [&self.request_headers, &self.response_headers] {
            for (name, values) in headers {
                if sensitive_header(name) {
                    for value in values {
                        remember(value);
                        if normalized(name).contains("auth") {
                            if let Some((scheme, token)) = value.split_once(' ') {
                                remember(token.trim());
                                if scheme.eq_ignore_ascii_case("basic") {
                                    if let Ok(bytes) = base64::engine::general_purpose::STANDARD
                                        .decode(token.trim())
                                    {
                                        if let Ok(text) = String::from_utf8(bytes) {
                                            remember(&text);
                                            for part in text.split(':') {
                                                remember(part);
                                            }
                                        }
                                    }
                                }
                            }
                            for part in value.split(',') {
                                if let Some((key, value)) = part.split_once('=') {
                                    let key = key.split_whitespace().last().unwrap_or("");
                                    if sensitive_header(key)
                                        || ["username", "response", "nonce", "cnonce"]
                                            .contains(&key)
                                    {
                                        remember(value.trim().trim_matches('"'));
                                    }
                                }
                            }
                        }
                        if normalized(name).contains("cookie") {
                            for pair in value.split(';') {
                                if let Some((_, value)) = pair.split_once('=') {
                                    remember(value.trim().trim_matches('"'));
                                }
                            }
                        }
                    }
                }
            }
        }
        let mut patterns = Vec::new();
        for value in secrets {
            patterns.extend([
                value.clone(),
                encoded(&value),
                encoded(&value).replace("%20", "+"),
                value.as_bytes().iter().map(|b| char::from(*b)).collect(),
            ]);
            let json = json!(value).to_string();
            patterns.push(json[1..json.len() - 1].into());
        }
        patterns.sort_by_key(|value| std::cmp::Reverse(value.len()));
        let redact = |text: &str| {
            if overflow {
                return "[redacted: credential capture limit exceeded]".to_string();
            }
            let mut text = text.to_string();
            for value in &patterns {
                text = text.replace(value, "[redacted]");
            }
            (self.owner.redact)(&text)
        };
        let headers = |raw: &BTreeMap<String, Vec<String>>| {
            let mut entries = Vec::new();
            let mut used = 0;
            let mut truncated = overflow;
            for (name, values) in raw {
                if entries.len() >= 128 || name.len() > 256 {
                    truncated = true;
                    continue;
                }
                used += name.len() + 32;
                let mut safe = Vec::new();
                for value in values {
                    let value = if sensitive_header(name) {
                        "[redacted]".into()
                    } else {
                        redact(&header_value(name, value))
                    };
                    if used + value.len() + 8 > HEADER {
                        truncated = true;
                        continue;
                    }
                    used += value.len() + 8;
                    safe.push(value);
                }
                if used > HEADER {
                    truncated = true;
                    continue;
                }
                entries.push(json!({"name":redact(name),"values":safe}));
            }
            json!({"entries":entries,"truncated":truncated})
        };
        let result = if failed {
            "request failed or cancelled".into()
        } else {
            format!("HTTP {status}")
        };
        (self.owner.sink)(
            json!({"message":redact(&format!("Request {} · {} {} · {} · {} ms",self.id,self.method,self.url,result,self.started.elapsed().as_millis())),"network":{"request_id":self.id.to_string(),"request":self.request.snapshot(&redact),"response":self.response.snapshot(&redact),"request_headers":headers(&self.request_headers),"response_headers":headers(&self.response_headers)}}),
            failed || status >= 400,
        );
    }
}
impl Drop for NetworkTrace {
    // Release owned resources when the instance leaves scope
    fn drop(&mut self) {
        self.finish(0, true);
    }
}

#[cfg(feature = "http")]
pub(crate) type HttpTrace = Option<Arc<Mutex<NetworkTrace>>>;
#[cfg(feature = "http")]
// Begin the HTTP diagnostic record
pub(crate) fn http_start(
    network: Option<&crate::NetworkPayload>,
    method: &str,
    url: &str,
    headers: &BTreeMap<String, String>,
) -> HttpTrace {
    network
        .and_then(|n| n.diagnostics.start(method, url, headers))
        .map(|t| Arc::new(Mutex::new(t)))
}
#[cfg(feature = "http")]
// Record response headers using the redaction policy
pub(crate) fn http_headers(trace: &HttpTrace, response: &reqwest::blocking::Response) {
    if let Some(trace) = trace {
        let mut headers = BTreeMap::<String, Vec<String>>::new();
        for (name, value) in response.headers() {
            headers
                .entry(name.to_string())
                .or_default()
                .push(String::from_utf8_lossy(value.as_bytes()).into_owned());
        }
        trace.lock().unwrap().received(headers);
    }
}
#[cfg(feature = "http")]
// Account for the next HTTP response chunk
pub(crate) fn http_read(trace: &HttpTrace, bytes: &[u8]) {
    if let Some(trace) = trace {
        let mut trace = trace.lock().unwrap();
        trace.response.write(bytes);
        if bytes.is_empty() {
            trace.response.complete = true;
        }
    }
}
#[cfg(feature = "http")]
// Finalize the HTTP diagnostic record
pub(crate) fn http_finish(trace: &HttpTrace, status: u16) {
    if let Some(trace) = trace {
        trace.lock().unwrap().finish(status, false);
    }
}
#[cfg(feature = "http")]
pub(crate) struct HttpUpload {
    pub data: std::io::Cursor<Vec<u8>>,
    pub network: Option<crate::NetworkPayload>,
    pub trace: HttpTrace,
}
#[cfg(feature = "http")]
impl std::io::Read for HttpUpload {
    // Read data through the owned transport or state backend for redacted network diagnostics
    fn read(&mut self, buffer: &mut [u8]) -> std::io::Result<usize> {
        let n = std::io::Read::read(&mut self.data, buffer)?;
        if let Some(network) = &self.network {
            network.add(n as u64, 0);
        }
        if let Some(trace) = &self.trace {
            let mut trace = trace.lock().unwrap();
            trace.request.write(&buffer[..n]);
            trace.request.complete = self.data.position() as usize == self.data.get_ref().len();
        }
        Ok(n)
    }
}
