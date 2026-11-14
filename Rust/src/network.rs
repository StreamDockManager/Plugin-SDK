use std::{
    io::{Read, Result, Write},
    sync::{Arc, Mutex},
};
/// Instrumented HTTP body/TCP payload only; exclude the control WebSocket
#[derive(Clone, Default)]
pub struct NetworkPayload {
    counters: Arc<Mutex<(u64, u64)>>,
    pub diagnostics: crate::diagnostics::NetworkDiagnostics,
}
impl NetworkPayload {
    // Copy the current observable state for network byte accounting
    pub fn snapshot(&self) -> (u64, u64) {
        *self.counters.lock().unwrap()
    }
    // Add transferred bytes to the shared counters
    pub fn add(&self, sent: u64, received: u64) {
        let mut c = self.counters.lock().unwrap();
        c.0 = c.0.saturating_add(sent);
        c.1 = c.1.saturating_add(received);
    }
    // Wrap transport I/O with byte accounting
    pub fn track<T>(&self, stream: T) -> Tracked<T> {
        Tracked {
            inner: stream,
            counters: self.clone(),
        }
    }
}
/// Wrap a stream/body once, before decompression. Ownership stays with the wrapper
pub struct Tracked<T> {
    pub inner: T,
    pub counters: NetworkPayload,
}
impl<T: Read> Read for Tracked<T> {
    // Read data through the owned transport or state backend for network byte accounting
    fn read(&mut self, buf: &mut [u8]) -> Result<usize> {
        let n = self.inner.read(buf)?;
        self.counters.add(0, n as u64);
        self.counters.diagnostics.io("read", n);
        Ok(n)
    }
}
impl<T: Write> Write for Tracked<T> {
    // Write data through the owned transport or state backend for network byte accounting
    fn write(&mut self, buf: &[u8]) -> Result<usize> {
        let n = self.inner.write(buf)?;
        self.counters.add(n as u64, 0);
        self.counters.diagnostics.io("write", n);
        Ok(n)
    }
    // Write pending state or bytes to the owned destination
    fn flush(&mut self) -> Result<()> {
        self.inner.flush()
    }
}
