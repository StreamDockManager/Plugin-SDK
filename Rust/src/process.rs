//! Exact argument vectors, bounded output, deadlines and cooperative cancellation
use crate::{error, Result};
use std::{
    io::Read,
    process::{Command, Stdio},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    thread,
    time::{Duration, Instant},
};
#[derive(Clone)]
pub struct Options {
    pub timeout: Duration,
    pub output_limit: usize,
    pub cancel: Arc<AtomicBool>,
    pub directory: Option<std::path::PathBuf>,
    pub environment: std::collections::BTreeMap<String, String>,
}
impl Default for Options {
    // Supply the default configuration without performing I/O for child process execution
    fn default() -> Self {
        Self {
            timeout: Duration::from_secs(8),
            output_limit: 1 << 20,
            cancel: Arc::new(AtomicBool::new(false)),
            directory: None,
            environment: Default::default(),
        }
    }
}
// Create a child command without foreground window activation
pub fn background_command(name: &str) -> Command {
    let mut command = Command::new(name);
    configure(&mut command);
    command
}
#[cfg(windows)]
// Apply the requested execution or diagnostics configuration
fn configure(command: &mut Command) {
    use std::os::windows::process::CommandExt;
    command.creation_flags(0x08000000);
}
#[cfg(not(windows))]
// Apply the requested execution or diagnostics configuration
fn configure(_: &mut Command) {}
// Run the configured operation until completion or shutdown
pub fn run(name: &str, args: &[&str], options: Options) -> Result<String> {
    if options.cancel.load(Ordering::Acquire) {
        return Err(error("process cancelled"));
    }
    let mut command = background_command(name);
    if let Some(directory) = options.directory {
        command.current_dir(directory);
    }
    command.envs(options.environment);
    let mut child = command
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()?;
    let limit = options.output_limit;
    let overflow = Arc::new(AtomicBool::new(false));
    let out = child.stdout.take().unwrap();
    let err = child.stderr.take().unwrap();
    // Read child output while enforcing the byte limit
    fn reader(
        mut pipe: impl Read + Send + 'static,
        limit: usize,
        overflow: Arc<AtomicBool>,
    ) -> thread::JoinHandle<Vec<u8>> {
        thread::spawn(move || {
            let mut output = Vec::new();
            let mut bytes = [0; 65536];
            while let Ok(n) = pipe.read(&mut bytes) {
                if n == 0 {
                    break;
                }
                let keep = n.min(limit.saturating_sub(output.len()));
                output.extend_from_slice(&bytes[..keep]);
                if keep < n {
                    overflow.store(true, Ordering::Release);
                }
            }
            output
        })
    }
    let stdout = reader(out, limit, overflow.clone());
    let stderr = reader(err, limit, overflow.clone());
    let deadline = Instant::now() + options.timeout;
    let status = loop {
        if let Some(status) = child.try_wait()? {
            break status;
        }
        if options.cancel.load(Ordering::Acquire)
            || Instant::now() >= deadline
            || overflow.load(Ordering::Acquire)
        {
            let _ = child.kill();
            let _ = child.wait();
            return Err(error(
                "process cancelled, timed out or exceeded output limit",
            ));
        }
        thread::sleep(Duration::from_millis(5));
    };
    let closing = Instant::now() + Duration::from_secs(1);
    while (!stdout.is_finished() || !stderr.is_finished()) && Instant::now() < closing {
        thread::sleep(Duration::from_millis(5));
    }
    if !stdout.is_finished() || !stderr.is_finished() {
        return Err(error("process output did not close"));
    }
    let output = stdout
        .join()
        .map_err(|_| error("process output reader failed"))?;
    let _ = stderr.join();
    if overflow.load(Ordering::Acquire) {
        return Err(error("process response exceeds limit"));
    }
    if !status.success() {
        return Err(error(format!(
            "{name} failed; check selection and operating-system permissions"
        )));
    }
    Ok(String::from_utf8(output)?)
}
