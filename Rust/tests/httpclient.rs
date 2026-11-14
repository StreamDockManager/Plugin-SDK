#![cfg(feature = "http")]
use serde_json::json;
use std::{
    io::{Read, Write},
    net::TcpListener,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Mutex,
    },
    thread,
    time::Duration,
};
use stream_dock_manager_plugin_sdk::{
    httpclient::{request, Options, ValidatorCache},
    NetworkPayload,
};
struct Server {
    url: String,
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}
impl Server {
    // Initialize the instance and its owned state
    fn new() -> Self {
        let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        listener.set_nonblocking(true).unwrap();
        let stop = Arc::new(AtomicBool::new(false));
        let done = stop.clone();
        let worker = thread::spawn(move || {
            while !done.load(Ordering::Acquire) {
                let (mut socket, _) = match listener.accept() {
                    Ok(v) => v,
                    Err(_) => {
                        thread::sleep(Duration::from_millis(2));
                        continue;
                    }
                };
                socket.set_nonblocking(false).unwrap();
                socket
                    .set_read_timeout(Some(Duration::from_secs(2)))
                    .unwrap();
                let mut raw = vec![];
                let mut byte = [0];
                while !raw.ends_with(b"\r\n\r\n") {
                    if socket.read(&mut byte).unwrap_or(0) == 0 {
                        break;
                    };
                    raw.push(byte[0]);
                    if raw.len() > 65536 {
                        break;
                    }
                }
                let text = String::from_utf8_lossy(&raw);
                let path = text.split_whitespace().nth(1).unwrap_or("/");
                let content_length = text
                    .lines()
                    .find_map(|line| {
                        line.to_lowercase()
                            .strip_prefix("content-length:")
                            .and_then(|v| v.trim().parse::<usize>().ok())
                    })
                    .unwrap_or(0);
                let mut body = vec![0; content_length];
                let _ = socket.read_exact(&mut body);
                let auth = text
                    .lines()
                    .find_map(|line| {
                        line.split_once(':')
                            .filter(|(name, _)| name.eq_ignore_ascii_case("authorization"))
                            .map(|(_, value)| value.trim())
                    })
                    .unwrap_or("")
                    .trim_start_matches("Bearer ");
                let mut payload = serde_json::to_vec(
                    &json!({"value":42,"echo":auth,"body":String::from_utf8_lossy(&body)}),
                )
                .unwrap();
                let mut status = "200 OK";
                let mut headers = String::new();
                if path == "/cache" {
                    headers += "ETag: \"v1\"\r\n";
                    if text.to_lowercase().contains("if-none-match: \"v1\"") {
                        status = "304 Not Modified";
                        payload.clear()
                    }
                }
                if path == "/broken304" {
                    status = "304 Not Modified";
                    payload.clear()
                }
                if path == "/redirect" {
                    status = "302 Found";
                    headers += "Location: /json\r\n";
                    payload.clear()
                }
                if path == "/outside" {
                    status = "302 Found";
                    headers += "Location: http://127.0.0.1:1/forbidden\r\n";
                    payload.clear()
                }
                if path == "/trailing" {
                    payload = b"{} trailing".to_vec();
                }
                if path == "/badvalidator" {
                    if text.to_lowercase().contains("if-none-match:") {
                        headers += "ETag: invalid\r\n";
                        status = "304 Not Modified";
                        payload.clear();
                    } else {
                        headers += "ETag: \"v1\"\r\n";
                    }
                }
                if path == "/large" {
                    payload = vec![b'x'; (1 << 20) + 1]
                }
                let header = format!(
                    "HTTP/1.1 {status}\r\n{headers}Content-Length: {}\r\nConnection: close\r\n\r\n",
                    payload.len()
                );
                let _ = socket.write_all(header.as_bytes());
                let _ = socket.write_all(&payload);
            }
        });
        Self {
            url,
            stop,
            worker: Some(worker),
        }
    }
    // Apply or retrieve the action settings in this scope
    fn settings(&self, path: &str) -> serde_json::Value {
        json!({"url":format!("{}{path}",self.url),"fields":{"value":"value","echo":"echo"}})
    }
}
impl Drop for Server {
    // Release owned resources when the instance leaves scope
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        self.worker.take().unwrap().join().unwrap()
    }
}
#[test]
// Verify real http redaction tracking and validator identity
fn real_http_redaction_tracking_and_validator_identity() {
    let server = Server::new();
    let network = NetworkPayload::default();
    let mut settings = server.settings("/json");
    settings["headers"] = json!({"Authorization":"Bearer private-token"});
    let result = request(
        &settings,
        &Options::default(),
        Arc::new(AtomicBool::new(false)),
        Some(network.clone()),
    )
    .unwrap();
    assert_eq!(result.fields["value"], 42);
    assert_eq!(result.fields["echo"], "[redacted]");
    assert!(network.snapshot().1 > 0);
    let mut settings = server.settings("/post");
    settings["method"] = json!("POST");
    settings["body"] = json!("payload");
    request(
        &settings,
        &Options::default(),
        Arc::new(AtomicBool::new(false)),
        Some(network.clone()),
    )
    .unwrap();
    assert_eq!(network.snapshot().0, 7);
    let options = Options {
        cache: Some(Arc::new(Mutex::new(ValidatorCache::default()))),
        automatic: true,
        ..Default::default()
    };
    let settings = server.settings("/cache");
    assert!(
        !request(&settings, &options, Arc::new(AtomicBool::new(false)), None)
            .unwrap()
            .revalidated
    );
    assert!(
        request(&settings, &options, Arc::new(AtomicBool::new(false)), None)
            .unwrap()
            .revalidated
    );
    let mut changed = settings.clone();
    changed["headers"] = json!({"Authorization":"Bearer other"});
    assert!(
        !request(&changed, &options, Arc::new(AtomicBool::new(false)), None)
            .unwrap()
            .revalidated
    );
    options.cache.as_ref().unwrap().lock().unwrap().reset();
    assert!(
        !request(&settings, &options, Arc::new(AtomicBool::new(false)), None)
            .unwrap()
            .revalidated
    );
    assert!(request(
        &server.settings("/broken304"),
        &options,
        Arc::new(AtomicBool::new(false)),
        None
    )
    .is_err());
}
#[test]
// Verify redirect bounds cancellation and automatic mutations
fn redirect_bounds_cancellation_and_automatic_mutations() {
    let server = Server::new();
    let call = |path, options: &Options| {
        request(
            &server.settings(path),
            options,
            Arc::new(AtomicBool::new(false)),
            None,
        )
    };
    assert_eq!(call("/redirect", &Options::default()).unwrap().status, 200);
    assert_eq!(
        call(
            "/redirect",
            &Options {
                no_redirect: true,
                ..Default::default()
            }
        )
        .unwrap()
        .status,
        302
    );
    assert!(call("/outside", &Options::default()).is_err());
    assert!(call("/large", &Options::default()).is_err());
    assert!(request(
        &server.settings("/json"),
        &Options::default(),
        Arc::new(AtomicBool::new(true)),
        None
    )
    .is_err());
    let mut settings = server.settings("/post");
    settings["method"] = json!("POST");
    assert!(request(
        &settings,
        &Options {
            automatic: true,
            ..Default::default()
        },
        Arc::new(AtomicBool::new(false)),
        None
    )
    .is_err());
}

#[test]
// Verify rejects trailing json and invalid validator
fn rejects_trailing_json_and_invalid_validator() {
    let server = Server::new();
    let options = Options {
        automatic: true,
        cache: Some(Arc::new(Mutex::new(ValidatorCache::default()))),
        ..Default::default()
    };
    let call = |path| {
        request(
            &server.settings(path),
            &options,
            Arc::new(AtomicBool::new(false)),
            None,
        )
    };
    assert!(call("/trailing").is_err());
    assert!(call("/badvalidator").is_ok());
    assert!(call("/badvalidator").is_err());
}

#[test]
// Verify webhook transport reuses live connection and closes explicitly
fn webhook_transport_reuses_live_connection_and_closes_explicitly() {
    use std::sync::atomic::AtomicUsize;
    use stream_dock_manager_plugin_sdk::httpclient::HttpTransport;
    let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
    listener.set_nonblocking(true).unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let accepted = Arc::new(AtomicUsize::new(0));
    let count = accepted.clone();
    let stop = Arc::new(AtomicBool::new(false));
    let done = stop.clone();
    let worker = thread::spawn(move || {
        while !done.load(Ordering::Acquire) {
            let Ok((mut connection, _)) = listener.accept() else {
                thread::sleep(Duration::from_millis(2));
                continue;
            };
            count.fetch_add(1, Ordering::AcqRel);
            connection.set_nonblocking(false).unwrap();
            connection
                .set_read_timeout(Some(Duration::from_secs(2)))
                .unwrap();
            loop {
                let mut request = Vec::new();
                let mut byte = [0];
                while !request.ends_with(b"\r\n\r\n") {
                    if connection.read(&mut byte).unwrap_or(0) == 0 {
                        return;
                    }
                    request.push(byte[0]);
                }
                connection
                    .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 12\r\n\r\n{\"value\":42}")
                    .unwrap();
            }
        }
    });
    let transport = Arc::new(HttpTransport::default());
    let options = Options {
        transport: Some(transport.clone()),
        ..Default::default()
    };
    for timeout in [1.0, 2.0, 0.5] {
        let result = request(
            &json!({"url":url,"timeout":timeout,"headers":{"Authorization":"Bearer request-only"}}),
            &options,
            Arc::new(AtomicBool::new(false)),
            None,
        )
        .unwrap();
        assert_eq!(result.preview["value"], 42);
    }
    assert_eq!(accepted.load(Ordering::Acquire), 1);
    transport.close();
    assert!(request(
        &json!({"url":url}),
        &options,
        Arc::new(AtomicBool::new(false)),
        None
    )
    .unwrap_err()
    .to_string()
    .contains("closed"));
    stop.store(true, Ordering::Release);
    drop(options);
    worker.join().unwrap();
}

#[test]
// Verify webhook origin eviction and concurrent close preserve active lease
fn webhook_origin_eviction_and_concurrent_close_preserve_active_lease() {
    use std::sync::{atomic::AtomicUsize, mpsc};
    use stream_dock_manager_plugin_sdk::{httpclient::HttpTransport, testkit::Peer};
    let accepted = Arc::new(AtomicUsize::new(0));
    let mut peers = Vec::new();
    for _ in 0..17 {
        let count = accepted.clone();
        peers.push(
            Peer::serve(move |mut socket| {
                count.fetch_add(1, Ordering::AcqRel);
                loop {
                    let mut raw = Vec::new();
                    let mut byte = [0];
                    while !raw.ends_with(b"\r\n\r\n") {
                        if socket.read(&mut byte).unwrap_or(0) == 0 {
                            return;
                        }
                        raw.push(byte[0]);
                    }
                    if socket
                        .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")
                        .is_err()
                    {
                        return;
                    }
                }
            })
            .unwrap(),
        );
    }
    let transport = Arc::new(HttpTransport::default());
    let options = Options {
        transport: Some(transport.clone()),
        ..Default::default()
    };
    for peer in &peers {
        request(
            &json!({"url":format!("http://{}",peer.address)}),
            &options,
            Arc::new(AtomicBool::new(false)),
            None,
        )
        .unwrap();
    }
    request(
        &json!({"url":format!("http://{}",peers[16].address)}),
        &options,
        Arc::new(AtomicBool::new(false)),
        None,
    )
    .unwrap();
    assert_eq!(accepted.load(Ordering::Acquire), 17);
    request(
        &json!({"url":format!("http://{}",peers[0].address)}),
        &options,
        Arc::new(AtomicBool::new(false)),
        None,
    )
    .unwrap();
    assert_eq!(
        accepted.load(Ordering::Acquire),
        18,
        "oldest origin pool must be evicted"
    );
    transport.close();

    let (entered, started) = mpsc::channel();
    let (release, wait) = mpsc::channel();
    let wait = Arc::new(Mutex::new(wait));
    let peer = Peer::serve(move |mut socket| {
        let mut raw = Vec::new();
        let mut byte = [0];
        while !raw.ends_with(b"\r\n\r\n") {
            if socket.read(&mut byte).unwrap_or(0) == 0 {
                return;
            }
            raw.push(byte[0]);
        }
        entered.send(()).unwrap();
        wait.lock()
            .unwrap()
            .recv_timeout(Duration::from_secs(2))
            .unwrap();
        socket
            .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")
            .unwrap();
    })
    .unwrap();
    let transport = Arc::new(HttpTransport::default());
    let owner = transport.clone();
    let url = format!("http://{}", peer.address);
    let endpoint = url.clone();
    let active = thread::spawn(move || {
        request(
            &json!({"url":endpoint}),
            &Options {
                transport: Some(owner),
                ..Default::default()
            },
            Arc::new(AtomicBool::new(false)),
            None,
        )
    });
    started.recv_timeout(Duration::from_secs(2)).unwrap();
    transport.close();
    assert!(request(
        &json!({"url":url}),
        &Options {
            transport: Some(transport),
            ..Default::default()
        },
        Arc::new(AtomicBool::new(false)),
        None
    )
    .is_err());
    release.send(()).unwrap();
    assert_eq!(active.join().unwrap().unwrap().status, 200);
}
