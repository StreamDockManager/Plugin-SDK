//! Optional service WebSockets with bounded requests and explicit authentication
use crate::{
    error,
    network::{NetworkPayload, Tracked},
    Result,
};
use serde_json::Value;
use std::{
    collections::HashMap,
    io::{self, Read, Write},
    net::{Shutdown, TcpStream, ToSocketAddrs},
    sync::{
        atomic::{AtomicBool, AtomicU8, Ordering},
        mpsc::{self, Receiver, SyncSender},
        Arc, Mutex,
    },
    time::{Duration, Instant},
};
use tungstenite::{protocol::WebSocketConfig, Message, WebSocket};
pub const MAX_FRAME: usize = 4 << 20;
pub const MAX_REQUEST: usize = 64 << 10;
#[derive(Debug)]
pub struct Rejected(pub String);
impl std::fmt::Display for Rejected {
    // Format the error for its caller
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "{} was rejected; check permissions and the selected resource",
            self.0
        )
    }
}
impl std::error::Error for Rejected {}
#[derive(Debug)]
struct Uncertain(crate::Error);
impl std::fmt::Display for Uncertain {
    // Format the error for its caller
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("command delivery is uncertain; check the server state before trying again")
    }
}
impl std::error::Error for Uncertain {
    // Expose the underlying failure for error chaining
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(self.0.as_ref())
    }
}
// Convert a protocol command failure into a typed error
pub fn command_error(failure: crate::Error) -> crate::Error {
    if failure.is::<Rejected>() {
        failure
    } else {
        Box::new(Uncertain(failure))
    }
}
// Construct the closed-session error
fn closed() -> crate::Error {
    error("WebSocket connection closed; reconnect to refresh state")
}
enum Stream {
    Plain(TcpStream),
    Tls(Box<rustls::StreamOwned<rustls::ClientConnection, TcpStream>>),
}
impl Read for Stream {
    // Read data through the owned transport or state backend for shared WebSocket sessions
    fn read(&mut self, b: &mut [u8]) -> io::Result<usize> {
        match self {
            Self::Plain(s) => s.read(b),
            Self::Tls(s) => s.read(b),
        }
    }
}
impl Write for Stream {
    // Write data through the owned transport or state backend for shared WebSocket sessions
    fn write(&mut self, b: &[u8]) -> io::Result<usize> {
        match self {
            Self::Plain(s) => s.write(b),
            Self::Tls(s) => s.write(b),
        }
    }
    // Write pending state or bytes to the owned destination
    fn flush(&mut self) -> io::Result<()> {
        match self {
            Self::Plain(s) => s.flush(),
            Self::Tls(s) => s.flush(),
        }
    }
}
/// Only the authentication callback accesses the channel before session ownership
pub struct Channel {
    socket: WebSocket<Tracked<Stream>>,
    deadline: Instant,
}
impl Channel {
    // Queue the outbound message for the owned connection
    pub fn send(&mut self, value: &Value) -> Result<()> {
        let text = serde_json::to_string(value)?;
        if text.len() > MAX_REQUEST {
            return Err(error("invalid or oversized service request"));
        }
        self.socket.send(Message::Text(text.into()))?;
        Ok(())
    }
    // Wait for the next received message
    pub fn receive(&mut self) -> Result<Value> {
        loop {
            if Instant::now() >= self.deadline {
                return Err(error("service authentication timed out"));
            }
            match self.socket.read() {
                Ok(Message::Text(text)) => return Ok(serde_json::from_str(&text)?),
                Ok(Message::Ping(_) | Message::Pong(_)) => self.socket.flush()?,
                Err(tungstenite::Error::Io(e))
                    if matches!(
                        e.kind(),
                        io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock
                    ) =>
                {
                    continue
                }
                _ => return Err(closed()),
            }
        }
    }
}
struct Pending {
    response: SyncSender<Result<Value>>,
    phase: AtomicU8,
}
struct Command {
    id: String,
    text: String,
    pending: Arc<Pending>,
}
struct Inner {
    socket: TcpStream,
    closed: AtomicBool,
    pending: Mutex<HashMap<String, Arc<Pending>>>,
    outbound: SyncSender<Command>,
    on_closed: Box<dyn Fn() + Send + Sync>,
}
#[derive(Clone)]
pub struct Session {
    inner: Arc<Inner>,
}
impl Session {
    // Check whether the connection remains usable
    pub fn alive(&self) -> bool {
        !self.inner.closed.load(Ordering::Acquire)
    }
    // Close the owned session and release pending work for shared WebSocket sessions
    pub fn close(&self) {
        if self.inner.closed.swap(true, Ordering::AcqRel) {
            return;
        }
        let _ = self.inner.socket.shutdown(Shutdown::Both);
        for (_, pending) in self.inner.pending.lock().unwrap().drain() {
            let _ = pending.response.try_send(Err(closed()));
        }
        let _ = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| (self.inner.on_closed)()));
    }
    // Perform a bounded request through the configured transport for shared WebSocket sessions
    pub fn request(&self, id: &str, value: &Value, cancel: &AtomicBool) -> Result<Value> {
        if cancel.load(Ordering::Acquire) {
            return Err(error("service request cancelled"));
        }
        if id.is_empty() || id.len() > 128 {
            return Err(error("invalid service request ID"));
        }
        let text = serde_json::to_string(value)?;
        if text.len() > MAX_REQUEST {
            return Err(error("invalid or oversized service request"));
        }
        let (response, receiver) = mpsc::sync_channel(1);
        let item = Arc::new(Pending {
            response,
            phase: AtomicU8::new(0),
        });
        {
            let mut pending = self.inner.pending.lock().unwrap();
            if !self.alive() {
                return Err(closed());
            }
            if pending.len() >= 32 || pending.contains_key(id) {
                return Err(error("duplicate request ID or service request queue full"));
            }
            pending.insert(id.into(), item.clone());
        }
        let result = (|| {
            self.inner
                .outbound
                .try_send(Command {
                    id: id.into(),
                    text,
                    pending: item.clone(),
                })
                .map_err(|_| error("service write queue full"))?;
            let deadline = Instant::now() + Duration::from_secs(8);
            loop {
                if cancel.load(Ordering::Acquire) || Instant::now() >= deadline {
                    if item.phase.load(Ordering::Acquire) == 1 {
                        self.close()
                    }
                    return Err(error("service request cancelled or timed out"));
                }
                match receiver.recv_timeout(Duration::from_millis(10)) {
                    Ok(result) => return result,
                    Err(mpsc::RecvTimeoutError::Disconnected) => return Err(closed()),
                    Err(mpsc::RecvTimeoutError::Timeout) => {
                        if !self.alive() {
                            return Err(closed());
                        }
                    }
                }
            }
        })();
        self.inner.pending.lock().unwrap().remove(id);
        result
    }
    // Run the configured operation until completion or shutdown
    fn run(
        &self,
        mut channel: Channel,
        receiver: Receiver<Command>,
        mut route: impl FnMut(&Value) -> Result<Option<String>>,
    ) {
        let mut pong = Instant::now();
        let mut ping = pong + Duration::from_secs(30);
        while self.alive() {
            for command in receiver.try_iter() {
                let valid = self
                    .inner
                    .pending
                    .lock()
                    .unwrap()
                    .get(&command.id)
                    .is_some_and(|item| Arc::ptr_eq(item, &command.pending));
                if !valid {
                    continue;
                }
                command.pending.phase.store(1, Ordering::Release);
                if channel
                    .socket
                    .send(Message::Text(command.text.into()))
                    .is_err()
                {
                    self.close();
                    return;
                }
                command.pending.phase.store(2, Ordering::Release);
            }
            let now = Instant::now();
            if now.duration_since(pong) >= Duration::from_secs(90) {
                break;
            }
            if now >= ping {
                if channel
                    .socket
                    .send(Message::Ping(Vec::new().into()))
                    .is_err()
                {
                    break;
                }
                ping = now + Duration::from_secs(30)
            }
            match channel.socket.read() {
                Ok(Message::Text(text)) => {
                    let value = match serde_json::from_str::<Value>(&text) {
                        Ok(v) => v,
                        Err(_) => break,
                    };
                    let id = match route(&value) {
                        Ok(id) => id,
                        Err(_) => break,
                    };
                    if let Some(id) = id {
                        if let Some(pending) = self.inner.pending.lock().unwrap().get(&id) {
                            let _ = pending.response.try_send(Ok(value));
                        }
                    }
                }
                Ok(Message::Pong(_)) => pong = Instant::now(),
                Ok(Message::Ping(_)) => {
                    if channel.socket.flush().is_err() {
                        break;
                    }
                }
                Err(tungstenite::Error::Io(e))
                    if matches!(
                        e.kind(),
                        io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock
                    ) => {}
                _ => break,
            }
        }
        self.close();
    }
}
#[derive(Default)]
struct ClientState {
    generation: u64,
    key: String,
    session: Option<Session>,
    connecting: Option<TcpStream>,
}
#[derive(Default)]
pub struct ServiceClient {
    state: Mutex<ClientState>,
    opening: Mutex<()>,
    pub network: NetworkPayload,
}
impl ServiceClient {
    // Close the owned session and release pending work for shared WebSocket sessions
    pub fn close(&self) {
        let (session, connecting) = {
            let mut state = self.state.lock().unwrap();
            state.generation += 1;
            state.key.clear();
            (state.session.take(), state.connecting.take())
        };
        if let Some(socket) = connecting {
            let _ = socket.shutdown(Shutdown::Both);
        }
        if let Some(session) = session {
            session.close()
        }
    }
    // Establish the connection using the configured trust policy
    pub fn connect(
        &self,
        key: &str,
        endpoint: &str,
        cancel: &AtomicBool,
        handshake: impl FnOnce(&mut Channel) -> Result<()>,
        route: impl FnMut(&Value) -> Result<Option<String>> + Send + 'static,
        on_closed: impl Fn() + Send + Sync + 'static,
    ) -> Result<(Session, bool)> {
        if cancel.load(Ordering::Acquire) {
            return Err(error("service connection cancelled"));
        }
        let _opening = self
            .opening
            .try_lock()
            .map_err(|_| error("WebSocket connection already opening"))?;
        {
            let state = self.state.lock().unwrap();
            if state.key == key {
                if let Some(session) = state.session.as_ref().filter(|s| s.alive()) {
                    return Ok((session.clone(), false));
                }
            }
        }
        let url = url::Url::parse(endpoint).map_err(|_| error("invalid WebSocket URL"))?;
        if !matches!(url.scheme(), "ws" | "wss")
            || url.host_str().is_none()
            || !url.username().is_empty()
            || url.password().is_some()
            || url.query().is_some()
            || url.fragment().is_some()
        {
            return Err(error(
                "use ws:// or wss:// without credentials or query parameters",
            ));
        }
        self.close();
        let generation = self.state.lock().unwrap().generation;
        let deadline = Instant::now() + Duration::from_secs(10);
        let host = url.host_str().unwrap().trim_matches(['[', ']']);
        let port = url.port_or_known_default().unwrap();
        let mut connected = None;
        for address in (host, port)
            .to_socket_addrs()
            .map_err(|_| error("WebSocket connection failed; check the server URL and network"))?
            .take(64)
        {
            if cancel.load(Ordering::Acquire) || Instant::now() >= deadline {
                return Err(closed());
            }
            if let Ok(socket) = TcpStream::connect_timeout(
                &address,
                deadline
                    .saturating_duration_since(Instant::now())
                    .min(Duration::from_secs(8)),
            ) {
                connected = Some(socket);
                break;
            }
        }
        let socket = connected.ok_or_else(closed)?;
        socket.set_read_timeout(Some(Duration::from_millis(50)))?;
        socket.set_write_timeout(Some(Duration::from_secs(8)))?;
        let shutdown = socket.try_clone()?;
        {
            let mut state = self.state.lock().unwrap();
            if state.generation != generation || cancel.load(Ordering::Acquire) {
                return Err(closed());
            }
            state.connecting = Some(socket.try_clone()?);
        }
        let stopped = AtomicBool::new(false);
        // Keep the handshake cancellation watcher scoped to the connection attempt
        let result = std::thread::scope(|scope| {
            scope.spawn(|| {
                while !stopped.load(Ordering::Acquire) {
                    if cancel.load(Ordering::Acquire) || Instant::now() >= deadline {
                        let _ = shutdown.shutdown(Shutdown::Both);
                        return;
                    }
                    std::thread::sleep(Duration::from_millis(10));
                }
            });
            let result = (|| {
                let stream = if url.scheme() == "wss" {
                    let mut roots = rustls::RootCertStore::empty();
                    for cert in rustls_native_certs::load_native_certs().certs {
                        roots.add(cert)?;
                    }
                    let config = rustls::ClientConfig::builder()
                        .with_root_certificates(roots)
                        .with_no_client_auth();
                    let name = rustls::pki_types::ServerName::try_from(host.to_string())?;
                    Stream::Tls(Box::new(rustls::StreamOwned::new(
                        rustls::ClientConnection::new(Arc::new(config), name)?,
                        socket,
                    )))
                } else {
                    Stream::Plain(socket)
                };
                let config = WebSocketConfig::default()
                    .max_message_size(Some(MAX_FRAME))
                    .max_frame_size(Some(MAX_FRAME));
                // The wrapper is above TLS: encrypted records and TLS negotiation are excluded
                let mut upgrade = tungstenite::client::client_with_config(
                    endpoint,
                    self.network.track(stream),
                    Some(config),
                );
                let (socket, _) = loop {
                    match upgrade {
                        Ok(value) => break value,
                        Err(tungstenite::HandshakeError::Interrupted(pending)) => {
                            if cancel.load(Ordering::Acquire) || Instant::now() >= deadline {
                                return Err(closed());
                            }
                            upgrade = pending.handshake();
                        }
                        Err(_) => {
                            return Err(error(
                                "WebSocket connection failed; check the server URL and network",
                            ))
                        }
                    }
                };
                let mut channel = Channel { socket, deadline };
                handshake(&mut channel)?;
                if cancel.load(Ordering::Acquire) || Instant::now() >= deadline {
                    return Err(closed());
                }
                let (outbound, receiver) = mpsc::sync_channel(32);
                let session = Session {
                    inner: Arc::new(Inner {
                        socket: shutdown.try_clone()?,
                        closed: AtomicBool::new(false),
                        pending: Mutex::new(HashMap::new()),
                        outbound,
                        on_closed: Box::new(on_closed),
                    }),
                };
                {
                    let mut state = self.state.lock().unwrap();
                    if state.generation != generation {
                        drop(state);
                        session.close();
                        return Err(closed());
                    }
                    state.key = key.into();
                    state.connecting = None;
                    state.session = Some(session.clone());
                }
                let owner = session.clone();
                if let Err(failure) = std::thread::Builder::new()
                    .name("sdk-service-websocket".into())
                    // Run the established session and close it even when a route callback panics
                    .spawn(move || {
                        let _ = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                            owner.run(channel, receiver, route)
                        }));
                        owner.close();
                    })
                {
                    session.close();
                    return Err(failure.into());
                }
                Ok((session, true))
            })();
            stopped.store(true, Ordering::Release);
            result
        });
        self.state.lock().unwrap().connecting = None;
        if result.is_err() {
            let _ = shutdown.shutdown(Shutdown::Both);
        }
        result
    }
}
impl Drop for ServiceClient {
    // Release owned resources when the instance leaves scope
    fn drop(&mut self) {
        self.close()
    }
}
