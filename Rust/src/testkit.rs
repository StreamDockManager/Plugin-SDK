//! Real dispatch and capability checks with a bounded recorder and fake clock
use crate::*;
use serde_json::Value;
#[derive(Clone, Default)]
pub struct Clock {
    value: std::sync::Arc<std::sync::atomic::AtomicU64>,
}
impl Clock {
    // Initialize the instance and its owned state for isolated test fixtures
    pub fn new(now: u64) -> Self {
        Self {
            value: std::sync::Arc::new(std::sync::atomic::AtomicU64::new(now)),
        }
    }
    // Read the test clock timestamp
    pub fn now(&self) -> u64 {
        self.value.load(std::sync::atomic::Ordering::Acquire)
    }
    // Advance the deterministic test clock
    pub fn advance(&self, milliseconds: u64) {
        use std::sync::atomic::Ordering::{AcqRel, Acquire};
        let mut prior = self.value.load(Acquire);
        loop {
            match self.value.compare_exchange_weak(
                prior,
                prior.saturating_add(milliseconds),
                AcqRel,
                Acquire,
            ) {
                Ok(_) => break,
                Err(value) => prior = value,
            }
        }
    }
}
pub struct Recorder {
    pub client: Client,
    receiver: OutboundReceiver,
    pub history: Vec<Value>,
}
impl Recorder {
    // Initialize the instance and its owned state for isolated test fixtures
    pub fn new(host: HostInfo) -> Self {
        let (client, receiver) = Client::new(host, "test-token".into());
        client.isolate_logs();
        Self {
            client,
            receiver,
            history: vec![],
        }
    }
    // Collect completed output from the owner for isolated test fixtures
    pub fn drain(&mut self) -> Vec<Value> {
        let mut values: Vec<_> = self.receiver.try_iter().collect();
        values.extend(self.client.drain_logs());
        self.history.extend(values.clone());
        values
    }
    // Assert the next recorded test message
    pub fn expect(&mut self, expected: &[Value]) -> Result<()> {
        let actual = self.drain();
        if actual != expected {
            return Err(crate::error(format!(
                "recorded events differ: {actual:?} != {expected:?}"
            )));
        };
        Ok(())
    }
    // Check action-context correlation in recorded output
    pub fn assert_contexts(&self, allowed: &[&str]) -> Result<()> {
        for message in &self.history {
            if !allowed.contains(&message["context"].as_str().unwrap_or("")) {
                return Err(crate::error("event escaped allowed contexts"));
            }
        }
        Ok(())
    }
    // Replay fixture events through the handler
    pub fn replay(&self, handler: &mut impl Handler, events: &[Event]) -> Result<()> {
        for event in events {
            if !self.client.observe_control(event) {
                self.client.dispatch(handler, event)?
            }
        }
        Ok(())
    }
    // Advance scheduled work on the owning event loop for isolated test fixtures
    pub fn poll(&self, handler: &mut impl Handler) -> Result<()> {
        self.client.drain_background();
        handler.poll(&self.client)
    }
}

/// Disposable TCP peer. Closing shuts down accepted sockets before joining the listener
pub struct Peer {
    pub address: std::net::SocketAddr,
    stop: std::sync::Arc<std::sync::atomic::AtomicBool>,
    connections: std::sync::Arc<std::sync::Mutex<Vec<std::net::TcpStream>>>,
    worker: Option<std::thread::JoinHandle<()>>,
}
impl Peer {
    // Serve the disposable fixture transport
    pub fn serve(handler: impl Fn(std::net::TcpStream) + Send + Sync + 'static) -> Result<Self> {
        let listener = std::net::TcpListener::bind(("127.0.0.1", 0))?;
        let address = listener.local_addr()?;
        listener.set_nonblocking(true)?;
        let stop = std::sync::Arc::new(std::sync::atomic::AtomicBool::new(false));
        let done = stop.clone();
        let connections = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
        let accepted = connections.clone();
        let handler = std::sync::Arc::new(handler);
        let worker = std::thread::spawn(move || {
            while !done.load(std::sync::atomic::Ordering::Acquire) {
                match listener.accept() {
                    Ok((socket, _)) => {
                        let _ = socket.set_nonblocking(false);
                        let _ = socket.set_read_timeout(Some(std::time::Duration::from_secs(2)));
                        let _ = socket.set_write_timeout(Some(std::time::Duration::from_secs(2)));
                        if let Ok(copy) = socket.try_clone() {
                            accepted.lock().unwrap().push(copy);
                        }
                        let handler = handler.clone();
                        std::thread::spawn(move || handler(socket));
                    }
                    Err(_) => std::thread::sleep(std::time::Duration::from_millis(5)),
                }
            }
        });
        Ok(Self {
            address,
            stop,
            connections,
            worker: Some(worker),
        })
    }
    // Close the owned session and release pending work for isolated test fixtures
    pub fn close(&mut self) {
        self.stop.store(true, std::sync::atomic::Ordering::Release);
        for socket in self.connections.lock().unwrap().drain(..) {
            let _ = socket.shutdown(std::net::Shutdown::Both);
        }
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}
impl Drop for Peer {
    // Release owned resources when the instance leaves scope
    fn drop(&mut self) {
        self.close();
    }
}
#[cfg(feature = "testkit-tls")]
// Build TLS contexts using only bundled test certificates
pub fn tls() -> Result<(
    std::sync::Arc<rustls::ServerConfig>,
    std::sync::Arc<rustls::ClientConfig>,
)> {
    use base64::Engine;
    // Decode a PEM fixture into DER bytes
    fn der(pem: &str) -> Result<Vec<u8>> {
        Ok(base64::engine::general_purpose::STANDARD.decode(
            pem.lines()
                .filter(|line| !line.starts_with("---"))
                .collect::<String>(),
        )?)
    }
    let cert = rustls::pki_types::CertificateDer::from(der(include_str!(
        "../testdata/tls/localhost-cert.pem"
    ))?);
    let key = rustls::pki_types::PrivateKeyDer::Pkcs8(rustls::pki_types::PrivatePkcs8KeyDer::from(
        der(include_str!("../testdata/tls/localhost-key.pem"))?,
    ));
    let server = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(vec![cert.clone()], key)?;
    let mut roots = rustls::RootCertStore::empty();
    roots.add(cert)?;
    let client = rustls::ClientConfig::builder()
        .with_root_certificates(roots)
        .with_no_client_auth();
    Ok((std::sync::Arc::new(server), std::sync::Arc::new(client)))
}
