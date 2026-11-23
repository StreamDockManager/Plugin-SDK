//! Optional bounded HTTP helpers. Enable `http` for the reqwest transport
use crate::{error, Result};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::{
    collections::BTreeMap,
    sync::{atomic::AtomicBool, Arc, Mutex},
    time::UNIX_EPOCH,
};
#[cfg(feature = "http")]
use std::{
    collections::{hash_map::RandomState, HashMap},
    hash::BuildHasher,
    sync::atomic::Ordering,
    time::{Duration, Instant, SystemTime},
};

#[cfg(feature = "http")]
// Check whether response caching is prohibited
fn no_store(value: &str) -> bool {
    value.split(',').any(|part| {
        part.trim()
            .split('=')
            .next()
            .unwrap_or("")
            .eq_ignore_ascii_case("no-store")
    })
}

#[derive(Clone, Debug)]
pub struct PathPart {
    pub key: String,
    pub index: Option<usize>,
}
// Parse the selected JSON extraction path
pub fn parse_path(path: &str) -> Result<Vec<PathPart>> {
    if path.len() > 8192 {
        return Err(error("path exceeds 8192 bytes"));
    }
    let chars: Vec<char> = path.chars().collect();
    let mut index = 0;
    let mut parts = vec![];
    while index < chars.len() {
        let bracket = chars[index] == '[';
        if bracket {
            index += 1
        };
        if index >= chars.len() {
            return Err(error("unclosed bracket"));
        }
        let literal = matches!(chars[index], '\'' | '"');
        let mut key = String::new();
        if literal && chars[index] == '"' {
            let tail: String = chars[index..].iter().collect();
            let mut decoder = serde_json::Deserializer::from_str(&tail).into_iter::<String>();
            key = decoder
                .next()
                .ok_or_else(|| error("unterminated quoted key"))??;
            index += tail[..decoder.byte_offset()].chars().count();
        } else if literal {
            let quote = chars[index];
            index += 1;
            while index < chars.len() && chars[index] != quote {
                let mut ch = chars[index];
                index += 1;
                if ch == '\\' {
                    if index >= chars.len() {
                        return Err(error("unfinished escape"));
                    };
                    ch = chars[index];
                    index += 1;
                    ch = match ch {
                        'n' => '\n',
                        'r' => '\r',
                        't' => '\t',
                        'u' => {
                            if index + 4 > chars.len() {
                                return Err(error("short unicode escape"));
                            };
                            let code = u32::from_str_radix(
                                &chars[index..index + 4].iter().collect::<String>(),
                                16,
                            )?;
                            index += 4;
                            char::from_u32(code).ok_or_else(|| error("invalid unicode scalar"))?
                        }
                        other => other,
                    }
                };
                key.push(ch)
            }
            if index >= chars.len() {
                return Err(error("unterminated quoted key"));
            };
            index += 1;
        } else {
            while index < chars.len() && !['.', '[', ']'].contains(&chars[index]) {
                let mut ch = chars[index];
                index += 1;
                if ch == '\\' {
                    if index >= chars.len() {
                        return Err(error("unfinished escape"));
                    };
                    ch = chars[index];
                    index += 1
                };
                key.push(ch)
            }
            if key.is_empty() {
                return Err(error("empty path component"));
            }
        }
        let numeric = !literal && key.chars().all(|c| c.is_ascii_digit());
        let array = if numeric {
            key.parse::<usize>().ok()
        } else {
            None
        };
        if bracket && !literal && array.is_none() {
            return Err(error("brackets require index or quoted key"));
        };
        parts.push(PathPart { key, index: array });
        if parts.len() > 128 {
            return Err(error("path exceeds 128 components"));
        }
        if bracket {
            if chars.get(index) != Some(&']') {
                return Err(error("expected closing bracket"));
            };
            index += 1
        };
        if index == chars.len() {
            break;
        };
        if chars[index] == '[' {
            continue;
        };
        if chars[index] != '.' {
            return Err(error("expected dot or bracket"));
        };
        index += 1;
        if index == chars.len() || ['.', '['].contains(&chars[index]) {
            return Err(error("expected component after dot"));
        }
    }
    Ok(parts)
}
// Extract the value at a validated JSON path
pub fn extract_path(root: &Value, path: &str) -> Result<Option<Value>> {
    let mut value = root;
    for part in parse_path(path)? {
        value = match value {
            Value::Object(map) => match map.get(&part.key) {
                Some(v) => v,
                None => return Ok(None),
            },
            Value::Array(values) => match part.index.and_then(|i| values.get(i)) {
                Some(v) => v,
                None => return Ok(None),
            },
            _ => return Ok(None),
        }
    }
    Ok(Some(value.clone()))
}
// List candidate JSON extraction paths
pub fn path_suggestions(root: &Value) -> Vec<String> {
    // Visit nested values while preserving their path context
    fn walk(value: &Value, path: String, depth: usize, out: &mut Vec<String>) {
        if out.len() >= 256 || depth > 12 {
            return;
        };
        out.push(path.clone());
        match value {
            Value::Object(map) => {
                let mut keys: Vec<_> = map.keys().collect();
                keys.sort();
                for key in keys {
                    let simple = !key.is_empty() && !key.chars().any(|c| ".[]\\\"'".contains(c));
                    let child = if simple {
                        if path.is_empty() {
                            key.clone()
                        } else {
                            format!("{path}.{key}")
                        }
                    } else {
                        format!("{path}[{}]", serde_json::to_string(key).unwrap())
                    };
                    walk(&map[key], child, depth + 1, out)
                }
            }
            Value::Array(values) => {
                for (i, item) in values.iter().take(8).enumerate() {
                    walk(item, format!("{path}[{i}]"), depth + 1, out)
                }
            }
            _ => {}
        }
    }
    let mut out = vec![];
    walk(root, String::new(), 0, &mut out);
    out
}
// Identify fields that must not appear in diagnostics
pub fn sensitive(key: &str) -> bool {
    let key = key.to_lowercase();
    [
        "token",
        "password",
        "secret",
        "credential",
        "authorization",
        "cookie",
        "api-key",
        "api_key",
    ]
    .iter()
    .any(|v| key.contains(v))
        || ["key", "signature"].contains(&key.as_str())
}
// Remove sensitive values from the returned diagnostic data
pub fn redact(value: &Value, secrets: &[String]) -> Value {
    // Visit nested values while preserving their path context
    fn walk(value: &Value, secrets: &[String], depth: u32) -> Value {
        if depth > 10 {
            return json!("[preview depth limit]");
        };
        match value {
            Value::Object(map) => Value::Object(
                map.iter()
                    .take(128)
                    .map(|(key, value)| {
                        (
                            key.clone(),
                            if sensitive(key) {
                                json!("[redacted]")
                            } else {
                                walk(value, secrets, depth + 1)
                            },
                        )
                    })
                    .collect(),
            ),
            Value::Array(values) => Value::Array(
                values
                    .iter()
                    .take(32)
                    .map(|v| walk(v, secrets, depth + 1))
                    .collect(),
            ),
            Value::String(text) => {
                let mut text = text.clone();
                for secret in secrets {
                    if !secret.is_empty() {
                        text = text.replace(secret, "[redacted]")
                    }
                }
                if text.len() > 4096 {
                    text = text.chars().take(4096).collect::<String>() + "…"
                };
                json!(text)
            }
            _ => value.clone(),
        }
    }
    let mut ordered = secrets.to_vec();
    ordered.sort_by_key(|v| std::cmp::Reverse(v.len()));
    walk(value, &ordered, 0)
}
// Expand configured placeholders without broadening their scope
pub fn expand(value: &str) -> Result<String> {
    resolve_environment(value, &mut vec![])
}
// Resolve configured environment references before the request
fn resolve_environment(value: &str, secrets: &mut Vec<String>) -> Result<String> {
    let chars: Vec<char> = value.chars().collect();
    let mut i = 0;
    let mut out = String::new();
    while i < chars.len() {
        if chars[i] != '$' {
            out.push(chars[i]);
            i += 1;
            continue;
        };
        i += 1;
        let mut name = String::new();
        if chars.get(i) == Some(&'{') {
            i += 1;
            while i < chars.len() && chars[i] != '}' {
                name.push(chars[i]);
                i += 1
            }
            if chars.get(i) != Some(&'}') {
                return Err(error("unfinished environment reference"));
            };
            i += 1;
        } else {
            while i < chars.len() && (chars[i].is_ascii_alphanumeric() || chars[i] == '_') {
                name.push(chars[i]);
                i += 1
            }
        };
        let resolved = std::env::var(&name).map_err(|_| {
            error(format!(
                "credential environment variable {name:?} unavailable"
            ))
        })?;
        secrets.push(resolved.clone());
        out.push_str(&resolved)
    }
    Ok(out)
}
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct HTTPResult {
    pub status: u16,
    pub duration_ms: u64,
    pub fields: BTreeMap<String, Value>,
    pub missing: Vec<String>,
    pub preview: Value,
    pub paths: Vec<String>,
    pub revalidated: bool,
    pub retry_at: Option<u64>,
}
#[cfg(feature = "http")]
#[derive(Clone)]
struct Entry {
    result: HTTPResult,
    etag: String,
    modified: String,
    target: u128,
    at: Instant,
    used: u64,
    size: usize,
}
#[cfg(feature = "http")]
pub struct ValidatorCache {
    entries: HashMap<u128, Entry>,
    hashes: [RandomState; 2],
    epoch: u64,
    sequence: u64,
}
#[cfg(feature = "http")]
impl Default for ValidatorCache {
    // Supply the default configuration without performing I/O for HTTP requests
    fn default() -> Self {
        Self {
            entries: HashMap::new(),
            hashes: [RandomState::new(), RandomState::new()],
            epoch: 0,
            sequence: 0,
        }
    }
}
#[cfg(feature = "http")]
impl ValidatorCache {
    // Reset retained state for the next lifecycle for HTTP requests
    pub fn reset(&mut self) {
        self.epoch += 1;
        self.entries.clear()
    }
    // Derive the identity used to share or store state for HTTP requests
    fn key(&self, value: &Value) -> u128 {
        let raw = serde_json::to_vec(value).unwrap();
        ((self.hashes[0].hash_one(&raw) as u128) << 64) | (self.hashes[1].hash_one(&raw) as u128)
    }
    // Record the start of an operation for HTTP requests
    fn begin(&mut self, identity: &Value) -> (u128, u64, Option<Entry>) {
        self.entries
            .retain(|_, entry| entry.at.elapsed() < Duration::from_secs(1800));
        let key = self.key(identity);
        self.sequence += 1;
        if let Some(entry) = self.entries.get_mut(&key) {
            entry.used = self.sequence
        };
        (key, self.epoch, self.entries.get(&key).cloned())
    }
    // Retain a validated HTTP cache response
    fn store(
        &mut self,
        ticket: (u128, u64),
        result: &HTTPResult,
        etag: String,
        modified: String,
        target: &str,
        forbidden: bool,
    ) {
        if ticket.1 != self.epoch {
            return;
        };
        self.entries.remove(&ticket.0);
        let mut result = result.clone();
        result.preview = Value::Null;
        result.paths.clear();
        let size = serde_json::to_vec(&result).unwrap().len() + etag.len() + modified.len() + 64;
        if forbidden || (etag.is_empty() && modified.is_empty()) || size > 256 << 10 {
            return;
        };
        while self.entries.len() >= 64
            || self.entries.values().map(|v| v.size).sum::<usize>() + size > 8 << 20
        {
            if let Some(key) = self
                .entries
                .iter()
                .min_by_key(|(_, v)| v.used)
                .map(|(k, _)| *k)
            {
                self.entries.remove(&key);
            } else {
                break;
            }
        }
        self.sequence += 1;
        let target = self.key(&json!(target));
        self.entries.insert(
            ticket.0,
            Entry {
                result,
                etag,
                modified,
                target,
                at: Instant::now(),
                used: self.sequence,
                size,
            },
        );
    }
}
#[cfg(not(feature = "http"))]
#[derive(Default)]
pub struct ValidatorCache;
#[cfg(not(feature = "http"))]
impl ValidatorCache {
    // Reset retained state for the next lifecycle for HTTP requests
    pub fn reset(&mut self) {}
}
/// Bounded owner of webhook HTTP connection pools, separate from generic Service
/// Credentials and deadlines belong to requests, never to a retained client
#[cfg(feature = "http")]
#[derive(Default)]
pub struct HttpTransport {
    state: Mutex<TransportState>,
    #[cfg(all(test, feature = "testkit-tls"))]
    test_tls: Option<rustls::ClientConfig>,
}
#[cfg(feature = "http")]
#[derive(Default)]
struct TransportState {
    closed: bool,
    clients: HashMap<String, (reqwest::blocking::Client, Instant)>,
}
#[cfg(feature = "http")]
impl HttpTransport {
    // Reuse or create the HTTP client for this trust identity
    fn client(&self, url: &reqwest::Url) -> Result<reqwest::blocking::Client> {
        let origin = url.origin().ascii_serialization();
        let mut state = self.state.lock().unwrap();
        if state.closed {
            return Err(error("webhook HTTP transport closed"));
        }
        if let Some((client, used)) = state.clients.get_mut(&origin) {
            *used = Instant::now();
            return Ok(client.clone());
        }
        let builder = reqwest::blocking::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .pool_max_idle_per_host(2)
            .pool_idle_timeout(Duration::from_secs(60));
        #[cfg(all(test, feature = "testkit-tls"))]
        let builder = if let Some(config) = &self.test_tls {
            builder.use_preconfigured_tls(config.clone())
        } else {
            builder
        };
        let client = builder.build()?;
        if state.clients.len() >= 16 {
            if let Some(oldest) = state
                .clients
                .iter()
                .min_by_key(|(_, (_, used))| *used)
                .map(|(key, _)| key.clone())
            {
                state.clients.remove(&oldest);
            }
        }
        state
            .clients
            .insert(origin, (client.clone(), Instant::now()));
        Ok(client)
    }
    /// Retire idle pools and reject further work. Active requests retain their own
    /// client lease and cancellation/deadline; they are never replayed
    pub fn close(&self) {
        let mut state = self.state.lock().unwrap();
        state.closed = true;
        state.clients.clear();
    }
}
#[cfg(feature = "http")]
// Construct the default HTTP transport with its TLS policy
fn default_transport() -> &'static HttpTransport {
    static TRANSPORT: std::sync::OnceLock<HttpTransport> = std::sync::OnceLock::new();
    TRANSPORT.get_or_init(HttpTransport::default)
}

#[derive(Default)]
pub struct Options {
    #[cfg(feature = "http")]
    pub transport: Option<Arc<HttpTransport>>,
    pub cache: Option<Arc<Mutex<ValidatorCache>>>,
    pub automatic: bool,
    pub status_only: bool,
    pub no_redirect: bool,
    pub allow_user_info: bool,
}
#[cfg(feature = "http")]
struct PreparedRequest {
    method: String,
    url: reqwest::Url,
    body: String,
    timeout: f64,
    fields: BTreeMap<String, String>,
    headers: reqwest::header::HeaderMap,
    secrets: Vec<String>,
    conditional: bool,
    identity: Value,
}

#[cfg(feature = "http")]
// Validate request inputs before acquiring transport resources
fn prepare_request(settings: &Value, options: &Options, preview: bool) -> Result<PreparedRequest> {
    let method = settings["method"].as_str().unwrap_or("GET").to_uppercase();
    if !["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"].contains(&method.as_str()) {
        return Err(error("unsupported HTTP method"));
    }
    if options.automatic && method != "GET" && method != "HEAD" {
        return Err(error("automatic HTTP requests must be GET or HEAD"));
    }
    let raw = expand(settings["url"].as_str().unwrap_or(""))?;
    let url = reqwest::Url::parse(&raw)?;
    if !["http", "https"].contains(&url.scheme())
        || url.host_str().is_none()
        || (!url.username().is_empty() || url.password().is_some()) && !options.allow_user_info
    {
        return Err(error(
            "request URL must be HTTP(S) without embedded credentials",
        ));
    }
    let body = expand(settings["body"].as_str().unwrap_or(""))?;
    let timeout = settings["timeout"]
        .as_f64()
        .or_else(|| settings["timeout"].as_str().and_then(|v| v.parse().ok()))
        .unwrap_or(10.);
    if !timeout.is_finite() || timeout <= 0. || timeout > 120. {
        return Err(error("invalid request timeout"));
    }
    let mut fields = BTreeMap::new();
    if let Some(map) = settings["fields"].as_object() {
        if map.len() > 32 {
            return Err(error("configure at most 32 fields"));
        };
        for (key, value) in map {
            let path = value
                .as_str()
                .ok_or_else(|| error("field path must be string"))?;
            parse_path(path)?;
            fields.insert(key.clone(), path.to_string());
        }
    }
    let mut headers = reqwest::header::HeaderMap::new();
    if let Some(map) = settings["headers"].as_object() {
        for (key, value) in map {
            headers.insert(
                reqwest::header::HeaderName::from_bytes(key.as_bytes())?,
                expand(
                    value
                        .as_str()
                        .ok_or_else(|| error("header must be string"))?,
                )?
                .parse()?,
            );
        }
    }
    // Inspect the current input for request preparation
    fn scan(value: &Value, key: &str, secrets: &mut Vec<String>) {
        match value {
            Value::Object(map) => {
                for (key, v) in map {
                    scan(v, key, secrets)
                }
            }
            Value::Array(values) => {
                for v in values {
                    scan(v, key, secrets)
                }
            }
            Value::String(text) => {
                if sensitive(key) {
                    secrets.push(text.clone());
                    if key.eq_ignore_ascii_case("authorization") {
                        if let Some((_, token)) = text.split_once(' ') {
                            secrets.push(token.into())
                        }
                    };
                    if key.eq_ignore_ascii_case("cookie") {
                        for token in text.split(';') {
                            if let Some((_, value)) = token.split_once('=') {
                                secrets.push(value.trim().into())
                            }
                        }
                    }
                };
                if text.contains('$') {
                    let _ = resolve_environment(text, secrets);
                }
            }
            _ => {}
        }
    }
    let mut secrets = vec![];
    scan(settings, "", &mut secrets);
    for (name, value) in &headers {
        scan(
            &json!(value.to_str().unwrap_or("")),
            name.as_str(),
            &mut secrets,
        )
    }
    for (name, value) in url.query_pairs() {
        scan(&json!(value), &name, &mut secrets)
    }
    if let Some(password) = url.password() {
        secrets.push(password.into())
    };
    if let Ok(value) = serde_json::from_str(&body) {
        scan(&value, "", &mut secrets)
    };
    let header_json: Value = Value::Object(
        headers
            .iter()
            .map(|(k, v)| (k.to_string(), json!(v.to_str().unwrap_or(""))))
            .collect(),
    );
    let conditional = [
        "if-match",
        "if-none-match",
        "if-modified-since",
        "if-unmodified-since",
        "if-range",
        "range",
    ]
    .iter()
    .any(|key| headers.contains_key(*key));
    let identity = json!([
        method,
        url.as_str(),
        header_json,
        body,
        fields,
        settings["field"],
        options.status_only,
        preview,
        options.no_redirect,
        options.allow_user_info,
        secrets
    ]);
    Ok(PreparedRequest {
        method,
        url,
        body,
        timeout,
        fields,
        headers,
        secrets,
        conditional,
        identity,
    })
}

#[cfg(feature = "http")]
// Share the request deadline, cancellation flag, and counters with response handling
struct RequestContext<'a> {
    cancel: &'a AtomicBool,
    network: &'a Option<crate::NetworkPayload>,
    started: Instant,
    timeout: f64,
}

#[cfg(feature = "http")]
// Execute the prepared request with bounded response handling
fn execute_request(
    url: reqwest::Url,
    method: &str,
    body: String,
    mut headers: reqwest::header::HeaderMap,
    options: &Options,
    context: &RequestContext<'_>,
) -> Result<(reqwest::blocking::Response, crate::diagnostics::HttpTrace)> {
    use std::io::Cursor;
    let RequestContext {
        cancel,
        network,
        started,
        timeout,
    } = *context;
    let origin = (
        url.scheme().to_string(),
        url.host_str().unwrap().to_string(),
        url.port_or_known_default(),
    );
    let client = options
        .transport
        .as_deref()
        .unwrap_or_else(|| default_transport())
        .client(&url)?;
    let mut target = url;
    let mut request_method = method.to_string();
    let mut request_body = body.into_bytes();
    let mut redirects = 0;
    let (response, trace) = loop {
        if cancel.load(Ordering::Acquire) || started.elapsed().as_secs_f64() >= timeout {
            return Err(error("request cancelled or timed out"));
        }
        let fields = headers
            .iter()
            .map(|(key, value)| {
                (
                    key.to_string(),
                    String::from_utf8_lossy(value.as_bytes()).into_owned(),
                )
            })
            .collect();
        let trace = crate::diagnostics::http_start(
            network.as_ref(),
            &request_method,
            target.as_str(),
            &fields,
        );
        if request_body.is_empty() {
            if let Some(trace) = &trace {
                trace.lock().unwrap().request.complete = true;
            }
        }
        let mut request = client
            .request(
                reqwest::Method::from_bytes(request_method.as_bytes())?,
                target.clone(),
            )
            .timeout(Duration::from_secs_f64(
                (timeout - started.elapsed().as_secs_f64()).max(0.001),
            ))
            .headers(headers.clone());
        if !request_body.is_empty() {
            request = request.body(reqwest::blocking::Body::sized(
                crate::diagnostics::HttpUpload {
                    data: Cursor::new(request_body.clone()),
                    network: network.clone(),
                    trace: trace.clone(),
                },
                request_body.len() as u64,
            ));
        }
        let response = request
            .send()
            .map_err(|_| error("request failed (check URL, network, TLS and redirect policy)"))?;
        crate::diagnostics::http_headers(&trace, &response);
        let status = response.status().as_u16();
        let location = response
            .headers()
            .get("location")
            .and_then(|value| value.to_str().ok());
        if options.no_redirect || ![301, 302, 303, 307, 308].contains(&status) || location.is_none()
        {
            break (response, trace);
        }
        // Observe each hop without consuming a redirect body just for diagnostics
        crate::diagnostics::http_finish(&trace, status);
        let next = target.join(location.unwrap())?;
        if (
            next.scheme(),
            next.host_str().unwrap_or(""),
            next.port_or_known_default(),
        ) != (origin.0.as_str(), origin.1.as_str(), origin.2)
            || redirects >= 4
        {
            return Err(error("cross-origin or excessive redirect"));
        }
        redirects += 1;
        if status == 303 && request_method != "HEAD"
            || [301, 302].contains(&status) && request_method == "POST"
        {
            request_method = "GET".into();
            request_body.clear();
            headers.remove("content-length");
            headers.remove("content-type");
        }
        target = next;
    };
    Ok((response, trace))
}

#[cfg(feature = "http")]
// Decode and redact the selected response value
fn extract_result(
    result: &mut HTTPResult,
    data: &[u8],
    method: &str,
    fields: BTreeMap<String, String>,
    secrets: &[String],
    preview: bool,
) -> Result<()> {
    if method != "HEAD" && !data.is_empty() {
        let mut decoder = serde_json::Deserializer::from_slice(data).into_iter::<Value>();
        if let Some(Ok(root)) = decoder.next() {
            if data[decoder.byte_offset()..]
                .iter()
                .any(|b| !b.is_ascii_whitespace())
            {
                return Err(error("response contains trailing JSON data"));
            }
            if preview {
                result.preview = redact(&root, secrets);
                result.paths = path_suggestions(&root);
            }
            for (name, path) in fields {
                if let Some(value) = extract_path(&root, &path)? {
                    result.fields.insert(name, redact(&value, secrets));
                } else {
                    result.missing.push(name)
                }
            }
        } else if preview {
            result.preview = redact(
                &json!(String::from_utf8_lossy(&data[..data.len().min(4096)])),
                secrets,
            )
        }
    }
    Ok(())
}

#[cfg(feature = "http")]
// Read the response body under its configured limit
fn read_response(
    response: &mut reqwest::blocking::Response,
    trace: &crate::diagnostics::HttpTrace,
    context: &RequestContext<'_>,
) -> Result<Vec<u8>> {
    use std::io::Read;
    let RequestContext {
        cancel,
        network,
        started,
        timeout,
    } = *context;
    let mut data = vec![];
    let mut block = [0u8; 65536];
    loop {
        if cancel.load(Ordering::Acquire) || started.elapsed().as_secs_f64() > timeout {
            return Err(error("request cancelled or timed out"));
        };
        let n = response.read(&mut block[..(1_048_577 - data.len()).min(65536)])?;
        crate::diagnostics::http_read(trace, &block[..n]);
        if let Some(counters) = &network {
            counters.add(0, n as u64)
        };
        data.extend_from_slice(&block[..n]);
        if data.len() > 1 << 20 {
            return Err(error("response exceeds 1 MiB"));
        };
        if n == 0 {
            break;
        }
    }
    Ok(data)
}

#[cfg(feature = "http")]
// Perform a bounded request through the configured transport for HTTP requests
pub fn request(
    settings: &Value,
    options: &Options,
    cancel: Arc<AtomicBool>,
    network: Option<crate::NetworkPayload>,
) -> Result<HTTPResult> {
    request_with_preview(settings, options, cancel, network, true)
}

#[cfg(feature = "http")]
/// Extract configured fields without constructing inspector previews or paths.
pub fn request_for_display(
    settings: &Value,
    options: &Options,
    cancel: Arc<AtomicBool>,
    network: Option<crate::NetworkPayload>,
) -> Result<HTTPResult> {
    request_with_preview(settings, options, cancel, network, false)
}

#[cfg(not(feature = "http"))]
pub use request as request_for_display;

#[cfg(feature = "http")]
fn request_with_preview(
    settings: &Value,
    options: &Options,
    cancel: Arc<AtomicBool>,
    network: Option<crate::NetworkPayload>,
    preview: bool,
) -> Result<HTTPResult> {
    let started = Instant::now();
    if cancel.load(Ordering::Acquire) {
        return Err(error("request cancelled or timed out"));
    }
    let PreparedRequest {
        method,
        url,
        body,
        timeout,
        fields,
        mut headers,
        secrets,
        conditional,
        identity,
    } = prepare_request(settings, options, preview)?;
    let mut ticket = if options.automatic && method == "GET" && !conditional {
        options
            .cache
            .as_ref()
            .map(|cache| cache.lock().unwrap().begin(&identity))
    } else {
        None
    };
    if headers
        .get("cache-control")
        .is_some_and(|v| no_store(v.to_str().unwrap_or("")))
    {
        if let (Some(cache), Some((key, _, _))) = (&options.cache, &ticket) {
            cache.lock().unwrap().entries.remove(key);
        };
        ticket = None;
    }
    if let Some((_, _, Some(prior))) = &ticket {
        if !prior.etag.is_empty() {
            headers.insert("if-none-match", prior.etag.parse()?);
        } else {
            headers.insert("if-modified-since", prior.modified.parse()?);
        }
    }
    let context = RequestContext {
        cancel: &cancel,
        network: &network,
        started,
        timeout,
    };
    let (mut response, trace) = execute_request(url, &method, body, headers, options, &context)?;
    if cancel.load(Ordering::Acquire) {
        return Err(error("request cancelled or timed out"));
    }
    let header = |key: &str| {
        response
            .headers()
            .get(key)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
            .to_string()
    };
    let forbidden =
        no_store(&header("cache-control")) || header("vary").split(',').any(|v| v.trim() == "*");
    let mut etag = header("etag");
    let mut modified = header("last-modified");
    let valid_etag = |value: &str| {
        let v = value.strip_prefix("W/").unwrap_or(value);
        value.len() <= 4096
            && v.starts_with('"')
            && v.ends_with('"')
            && v.len() >= 2
            && !v[1..v.len() - 1]
                .bytes()
                .any(|b| b < 0x21 || b == b'"' || b == 0x7f)
    };
    let invalid_validators = (!etag.is_empty() && !valid_etag(&etag))
        || (!modified.is_empty() && httpdate::parse_http_date(&modified).is_err());
    if !etag.is_empty() && !valid_etag(&etag) {
        etag.clear()
    };
    if !modified.is_empty() && httpdate::parse_http_date(&modified).is_err() {
        modified.clear()
    }
    let retry_at = crate::httpclient::parse_retry_after(
        &header("retry-after"),
        SystemTime::now().duration_since(UNIX_EPOCH)?.as_millis() as u64,
    );
    let mut result = HTTPResult {
        status: response.status().as_u16(),
        duration_ms: started.elapsed().as_millis() as u64,
        retry_at,
        ..Default::default()
    };
    if options.automatic && method == "GET" && result.status == 304 {
        let (key, epoch, prior) =
            ticket.ok_or_else(|| error("HTTP 304 has no matching retained response"))?;
        let prior = prior.ok_or_else(|| error("HTTP 304 has no matching retained response"))?;
        let mut cache = options.cache.as_ref().unwrap().lock().unwrap();
        let valid = !invalid_validators
            && cache.epoch == epoch
            && cache
                .entries
                .get(&key)
                .is_some_and(|current| current.used == prior.used)
            && cache.key(&json!(response.url().as_str())) == prior.target
            && (etag.is_empty()
                || prior.etag.is_empty()
                || etag.trim_start_matches("W/") == prior.etag.trim_start_matches("W/"))
            && (!prior.etag.is_empty() || modified.is_empty() || modified == prior.modified);
        if !valid {
            cache.entries.remove(&key);
            return Err(error("HTTP 304 has no matching retained response"));
        }
        crate::diagnostics::http_finish(&trace, result.status);
        let mut old = prior.result;
        old.status = result.status;
        old.duration_ms = result.duration_ms;
        old.revalidated = true;
        old.retry_at = result.retry_at;
        if etag.is_empty() {
            etag = prior.etag
        };
        if modified.is_empty() {
            modified = prior.modified
        };
        cache.store(
            (key, epoch),
            &old,
            etag,
            modified,
            response.url().as_str(),
            forbidden,
        );
        return Ok(old);
    }
    if !options.status_only {
        let data = read_response(&mut response, &trace, &context)?;
        extract_result(&mut result, &data, &method, fields, &secrets, preview)?;
    }
    crate::diagnostics::http_finish(&trace, result.status);
    result.duration_ms = started.elapsed().as_millis() as u64;
    if let (Some((key, epoch, _)), Some(cache)) = (ticket, &options.cache) {
        if result.status == 200 || forbidden {
            cache.lock().unwrap().store(
                (key, epoch),
                &result,
                etag,
                modified,
                response.url().as_str(),
                forbidden,
            )
        }
    };
    Ok(result)
}
#[cfg(not(feature = "http"))]
// Perform a bounded request through the configured transport for HTTP requests
pub fn request(
    _settings: &Value,
    _options: &Options,
    _cancel: Arc<AtomicBool>,
    _network: Option<crate::NetworkPayload>,
) -> Result<HTTPResult> {
    Err(error(
        "enable stream-dock-manager-plugin-sdk's http Cargo feature to make HTTP requests",
    ))
}
/// HTTP Retry-After, bounded to the largest whole-second Go duration
pub fn parse_retry_after(value: &str, now: u64) -> Option<u64> {
    const MAXIMUM: u64 = 9_223_372_036_000;
    let value = value.trim_matches([' ', '\t', '\r', '\n']);
    if !value.is_empty() && value.bytes().all(|b| b.is_ascii_digit()) {
        // Ignore leading zeroes before conversion so arbitrarily padded small values stay small
        let digits = value.trim_start_matches('0');
        let seconds = if digits.is_empty() {
            0
        } else {
            digits.parse::<u64>().unwrap_or(u64::MAX)
        };
        return Some(now.saturating_add(seconds.min(MAXIMUM / 1000) * 1000));
    }
    // Reject non-ASCII padding before httpdate's broader whitespace trimming
    if !value.is_ascii() || value.bytes().any(|b| b.is_ascii_control()) {
        return None;
    }
    httpdate::parse_http_date(value).ok().map(|date| {
        let at = date
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_millis().min(u64::MAX as u128) as u64)
            .unwrap_or(0);
        now.saturating_add(at.saturating_sub(now).min(MAXIMUM))
    })
}

#[cfg(all(test, feature = "http", feature = "testkit-tls"))]
mod transport_tests {
    use super::*;
    use std::{
        io::{Read, Write},
        sync::atomic::AtomicUsize,
    };
    #[test]
    // Verify persistent tls reuse and owner trust isolation
    fn persistent_tls_reuse_and_owner_trust_isolation() {
        let (server, client) = crate::testkit::tls().unwrap();
        let accepts = Arc::new(AtomicUsize::new(0));
        let count = accepts.clone();
        let peer = crate::testkit::Peer::serve(move |socket| {
            count.fetch_add(1, Ordering::AcqRel);
            let connection = rustls::ServerConnection::new(server.clone()).unwrap();
            let mut stream = rustls::StreamOwned::new(connection, socket);
            loop {
                let mut request = Vec::new();
                let mut byte = [0];
                while !request.ends_with(b"\r\n\r\n") {
                    if stream.read(&mut byte).unwrap_or(0) == 0 {
                        return;
                    }
                    request.push(byte[0]);
                }
                if stream
                    .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 12\r\n\r\n{\"value\":42}")
                    .is_err()
                {
                    return;
                }
                let _ = stream.flush();
            }
        })
        .unwrap();
        let transport = Arc::new(HttpTransport {
            test_tls: Some((*client).clone()),
            ..Default::default()
        });
        let options = Options {
            transport: Some(transport.clone()),
            ..Default::default()
        };
        let settings = json!({"url":format!("https://localhost:{}",peer.address.port())});
        for _ in 0..3 {
            assert_eq!(
                request(&settings, &options, Arc::new(AtomicBool::new(false)), None)
                    .unwrap()
                    .preview["value"],
                42
            );
        }
        assert_eq!(accepts.load(Ordering::Acquire), 1);
        let other = Options {
            transport: Some(Arc::new(HttpTransport::default())),
            ..Default::default()
        };
        assert!(request(&settings, &other, Arc::new(AtomicBool::new(false)), None).is_err());
        assert_eq!(
            request(&settings, &options, Arc::new(AtomicBool::new(false)), None)
                .unwrap()
                .preview["value"],
            42
        );
        assert_eq!(accepts.load(Ordering::Acquire), 2);
        transport.close();
    }
}

#[cfg(all(test, feature = "http"))]
mod preparation_tests {
    use super::*;
    #[test]
    // Verify preparation preserves validation order and credential identity
    fn preparation_preserves_validation_order_and_credential_identity() {
        let options = Options::default();
        let bad = json!({"method":"INVALID", "url":"bad", "timeout":-1});
        assert!(prepare_request(&bad, &options, true)
            .err()
            .unwrap()
            .to_string()
            .contains("method"));
        let base = json!({"url":"https://example.test/data", "headers":{"Authorization":"Bearer private"}, "fields":{"v":"value"}});
        let first = prepare_request(&base, &options, true).unwrap();
        assert!(first.secrets.iter().any(|v| v == "private"));
        let mut changed = base.clone();
        changed["headers"]["Authorization"] = json!("Bearer other");
        assert_ne!(
            first.identity,
            prepare_request(&changed, &options, true).unwrap().identity
        );
        let mut conditional = base;
        conditional["headers"]["If-None-Match"] = json!("\"v1\"");
        assert!(
            prepare_request(&conditional, &options, true)
                .unwrap()
                .conditional
        );
        let invalid = json!({"url":"https://example.test/", "timeout":121});
        assert!(prepare_request(&invalid, &options, true)
            .err()
            .unwrap()
            .to_string()
            .contains("timeout"));
    }
    #[test]
    // Verify extraction retains redaction missing fields and trailing data rejection
    fn extraction_retains_redaction_missing_fields_and_trailing_data_rejection() {
        let mut result = HTTPResult::default();
        let fields = BTreeMap::from([
            ("value".into(), "value".into()),
            ("missing".into(), "missing".into()),
        ]);
        extract_result(
            &mut result,
            br#"{"value":"private"}"#,
            "GET",
            fields,
            &["private".into()],
            true,
        )
        .unwrap();
        assert!(!result.preview.to_string().contains("private"));
        let mut display = HTTPResult::default();
        extract_result(
            &mut display,
            br#"{"value":"hello"}"#,
            "GET",
            BTreeMap::from([("selected".into(), "value".into())]),
            &[],
            false,
        )
        .unwrap();
        assert_eq!(display.fields["selected"], "hello");
        assert!(display.preview.is_null() && display.paths.is_empty());
        assert_eq!(result.missing, vec!["missing"]);
        assert!(extract_result(&mut result, b"{} {}", "GET", BTreeMap::new(), &[], true).is_err());
        assert!(extract_result(&mut result, b"{} {}", "HEAD", BTreeMap::new(), &[], true).is_ok());
    }
}
