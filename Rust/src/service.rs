//! Service HTTP API; enable the existing `http` feature for transport
use crate::{error, Result};
use serde_json::Value;
use std::{
    collections::BTreeMap,
    sync::{atomic::AtomicBool, Arc},
};
#[derive(Debug, Clone)]
pub struct HTTPError {
    pub status: u16,
    pub retry_at: Option<u64>,
}
impl std::fmt::Display for HTTPError {
    // Format the error for its caller
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "Service returned HTTP {}; check credentials, permissions and selection",
            self.status
        )
    }
}
impl std::error::Error for HTTPError {}
pub struct Service {
    pub limit: usize,
    pub timeout_ms: u64,
    pub network: crate::NetworkPayload,
    #[cfg(feature = "http")]
    client: std::sync::Mutex<Option<reqwest::blocking::Client>>,
}
impl Default for Service {
    // Supply the default configuration without performing I/O for service HTTP calls
    fn default() -> Self {
        Self {
            limit: 4 << 20,
            timeout_ms: 10000,
            network: crate::NetworkPayload::default(),
            #[cfg(feature = "http")]
            client: std::sync::Mutex::new(None),
        }
    }
}
impl Service {
    #[cfg(feature = "http")]
    // Perform a bounded request through the configured transport for service HTTP calls
    pub fn request(
        &self,
        method: &str,
        endpoint: &str,
        headers: &BTreeMap<String, String>,
        body: &[u8],
        cancel: Arc<AtomicBool>,
    ) -> Result<Vec<u8>> {
        use std::{
            io::Read,
            sync::atomic::Ordering,
            time::{Duration, SystemTime, UNIX_EPOCH},
        };
        let url = reqwest::Url::parse(endpoint)?;
        if !["http", "https"].contains(&url.scheme())
            || url.host_str().is_none()
            || !url.username().is_empty()
            || url.password().is_some()
            || url.fragment().is_some()
        {
            return Err(error("invalid service endpoint"));
        }
        if cancel.load(Ordering::Acquire) {
            return Err(error("request cancelled"));
        }
        let client = {
            let mut cached = self.client.lock().unwrap();
            if cached.is_none() {
                *cached = Some(
                    reqwest::blocking::Client::builder()
                        .redirect(reqwest::redirect::Policy::none())
                        .build()?,
                );
            }
            cached.as_ref().unwrap().clone()
        };
        let trace = crate::diagnostics::http_start(Some(&self.network), method, endpoint, headers);
        let mut request = client
            .request(reqwest::Method::from_bytes(method.as_bytes())?, url)
            .timeout(Duration::from_millis(self.timeout_ms))
            .body(reqwest::blocking::Body::sized(
                crate::diagnostics::HttpUpload {
                    data: std::io::Cursor::new(body.to_vec()),
                    network: Some(self.network.clone()),
                    trace: trace.clone(),
                },
                body.len() as u64,
            ));
        for (key, value) in headers {
            request = request.header(key, value);
        }
        let mut response = request
            .send()
            .map_err(|_| error("service connection failed; check address, TLS and credentials"))?;
        crate::diagnostics::http_headers(&trace, &response);
        let status = response.status().as_u16();
        let failure = if !(200..300).contains(&status) {
            let now = SystemTime::now().duration_since(UNIX_EPOCH)?.as_millis() as u64;
            let retry = response
                .headers()
                .get("Retry-After")
                .and_then(|x| x.to_str().ok())
                .and_then(|x| crate::httpclient::parse_retry_after(x, now));
            Some(HTTPError {
                status,
                retry_at: retry,
            })
        } else {
            None
        };
        let mut data = Vec::new();
        let mut chunk = [0u8; 65536];
        loop {
            if cancel.load(Ordering::Acquire) {
                return Err(error("request cancelled"));
            }
            let n = response.read(&mut chunk)?;
            crate::diagnostics::http_read(&trace, &chunk[..n]);
            if n == 0 {
                break;
            }
            self.network.add(0, n as u64);
            if data.len() + n > self.limit {
                return Err(error("invalid or oversized service response"));
            }
            data.extend_from_slice(&chunk[..n]);
        }
        crate::diagnostics::http_finish(&trace, status);
        if let Some(failure) = failure {
            return Err(Box::new(failure));
        }
        Ok(data)
    }
    #[cfg(not(feature = "http"))]
    // Perform a bounded request through the configured transport for service HTTP calls
    pub fn request(
        &self,
        _method: &str,
        _endpoint: &str,
        _headers: &BTreeMap<String, String>,
        _body: &[u8],
        _cancel: Arc<AtomicBool>,
    ) -> Result<Vec<u8>> {
        Err(error("enable SDK http feature"))
    }
    // Decode a service response as bounded JSON
    pub fn json(
        &self,
        method: &str,
        endpoint: &str,
        headers: &BTreeMap<String, String>,
        body: Option<&Value>,
        cancel: Arc<AtomicBool>,
    ) -> Result<Value> {
        let mut headers = headers.clone();
        if body.is_some()
            && !headers
                .keys()
                .any(|key| key.eq_ignore_ascii_case("content-type"))
        {
            headers.insert("Content-Type".into(), "application/json".into());
        }
        let bytes = body
            .map(serde_json::to_vec)
            .transpose()?
            .unwrap_or_default();
        let data = self.request(method, endpoint, &headers, &bytes, cancel)?;
        if data.is_empty() {
            Ok(Value::Null)
        } else {
            serde_json::from_slice(&data).map_err(|_| error("malformed service response"))
        }
    }
}
