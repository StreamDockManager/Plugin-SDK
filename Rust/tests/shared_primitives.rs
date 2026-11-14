use stream_dock_manager_plugin_sdk::httpclient::parse_retry_after;
#[test]
// Verify retry after contract
fn retry_after_contract() {
    let fixture: serde_json::Value =
        serde_json::from_str(include_str!("../testdata/retry-after-v1.json")).unwrap();
    let now = fixture["now_ms"].as_u64().unwrap();
    for case in fixture["cases"].as_array().unwrap() {
        assert_eq!(
            parse_retry_after(case["value"].as_str().unwrap(), now),
            case["delay_ms"].as_u64().map(|delay| now + delay),
            "{}",
            case["name"]
        );
    }
}

#[cfg(feature = "http")]
#[test]
// Verify service uses bounded parser for padded values and past dates
fn service_uses_bounded_parser_for_padded_values_and_past_dates() {
    use std::{
        collections::BTreeMap,
        io::{Read, Write},
        net::TcpListener,
        sync::{atomic::AtomicBool, Arc},
        time::{SystemTime, UNIX_EPOCH},
    };
    use stream_dock_manager_plugin_sdk::service::{HTTPError, Service};
    for (header, delay) in [
        (" 9223372037 ", 9_223_372_036_000),
        ("Tue, 31 Dec 2024 23:59:59 GMT", 0),
        ("+12", 0),
    ] {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let worker = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut request = [0; 4096];
            assert!(stream.read(&mut request).unwrap() > 0);
            write!(stream, "HTTP/1.1 429 Too Many Requests\r\nRetry-After: {header}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").unwrap();
        });
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_millis() as u64;
        let error = Service::default()
            .request(
                "GET",
                &url,
                &BTreeMap::new(),
                &[],
                Arc::new(AtomicBool::new(false)),
            )
            .unwrap_err();
        let error = error.downcast_ref::<HTTPError>().unwrap();
        if header == "+12" {
            assert_eq!(error.retry_at, None);
        } else {
            assert!(error.retry_at.unwrap() >= now + delay);
            assert!(error.retry_at.unwrap() < now + delay + 1000);
        }
        worker.join().unwrap();
    }
}

#[test]
// Verify retry timestamp saturates at integer boundary
fn retry_timestamp_saturates_at_integer_boundary() {
    assert_eq!(
        parse_retry_after("9223372037", u64::MAX - 1),
        Some(u64::MAX)
    );
    assert_eq!(parse_retry_after("0", u64::MAX - 1), Some(u64::MAX - 1));
}
