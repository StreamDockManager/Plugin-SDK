use std::sync::{
    atomic::{AtomicBool, Ordering},
    mpsc::{sync_channel, SyncSender},
    Arc, Mutex,
};
type Work = Box<dyn FnOnce() + Send + 'static>;
#[derive(Default)]
pub struct ProbePool {
    sender: Option<SyncSender<(Arc<AtomicBool>, Work)>>,
    stop: Arc<AtomicBool>,
}
impl ProbePool {
    // Admit work to the bounded worker queue
    pub fn submit(
        &mut self,
        cancel: Arc<AtomicBool>,
        work: impl FnOnce() + Send + 'static,
    ) -> bool {
        if self.stop.load(Ordering::Acquire) || cancel.load(Ordering::Acquire) {
            return false;
        }
        if self.sender.is_none() {
            let (tx, rx) = sync_channel::<(Arc<AtomicBool>, Work)>(32);
            let receiver = Arc::new(Mutex::new(rx));
            for _ in 0..4 {
                let rx = receiver.clone();
                let stop = self.stop.clone();
                // Drain bounded jobs while rejecting cancelled work and containing callback panics
                std::thread::spawn(move || {
                    while !stop.load(Ordering::Acquire) {
                        let job = rx
                            .lock()
                            .unwrap()
                            .recv_timeout(std::time::Duration::from_millis(50));
                        match job {
                            Ok((cancel, work))
                                if !stop.load(Ordering::Acquire)
                                    && !cancel.load(Ordering::Acquire) =>
                            {
                                let _ =
                                    std::panic::catch_unwind(std::panic::AssertUnwindSafe(work));
                            }
                            Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => break,
                            _ => {}
                        }
                    }
                });
            }
            self.sender = Some(tx);
        }
        self.sender
            .as_ref()
            .unwrap()
            .try_send((cancel, Box::new(work)))
            .is_ok()
    }
    // Signal worker shutdown and disconnect the submission queue
    pub fn close(&mut self) {
        self.stop.store(true, Ordering::Release);
        self.sender.take();
    }
}
impl Drop for ProbePool {
    // Release owned resources when the instance leaves scope
    fn drop(&mut self) {
        self.close()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{sync::mpsc::channel, time::Duration};

    #[test]
    // Verify four workers thirty two queued jobs and close rejects more
    fn four_workers_thirty_two_queued_jobs_and_close_rejects_more() {
        let mut pool = ProbePool::default();
        let release = Arc::new(AtomicBool::new(false));
        let (started, ready) = channel();
        let (finished, done) = channel();
        for _ in 0..4 {
            let release = release.clone();
            let started = started.clone();
            let finished = finished.clone();
            assert!(pool.submit(Arc::new(AtomicBool::new(false)), move || {
                started.send(()).unwrap();
                while !release.load(Ordering::Acquire) {
                    std::thread::yield_now()
                }
                finished.send(()).unwrap();
            }));
            ready.recv_timeout(Duration::from_secs(2)).unwrap();
        }
        let queued = Arc::new(AtomicBool::new(false));
        for _ in 0..32 {
            let queued = queued.clone();
            assert!(pool.submit(Arc::new(AtomicBool::new(false)), move || {
                queued.store(true, Ordering::Release);
            }));
        }
        assert!(!pool.submit(Arc::new(AtomicBool::new(false)), || {}));
        pool.close();
        release.store(true, Ordering::Release);
        for _ in 0..4 {
            done.recv_timeout(Duration::from_secs(2)).unwrap();
        }
        assert!(!queued.load(Ordering::Acquire));
        assert!(!pool.submit(Arc::new(AtomicBool::new(false)), || {}));
    }
}
