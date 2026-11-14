use serde_json::json;
use stream_dock_manager_plugin_sdk::{Client, Event, Handler, Result};
#[derive(Default)]
struct Advanced {
    ticks: i64,
}
impl Handler for Advanced {
    // Dispatch the received event to the registered behavior for advanced
    fn handle(&mut self, c: &Client, e: &Event) -> Result<()> {
        match e.event.as_str() {
            "willAppear" if c.supports("telemetry-v1") => c.subscribe_telemetry(&["cpu"]),
            "willAppear" => c.title(&e.context, "Telemetry unavailable on this host"),
            "dialRotate" => {
                self.ticks += e.payload["ticks"].as_i64().unwrap_or(0);
                c.title(&e.context, &self.ticks.to_string())
            }
            "touchTap" => c.title(&e.context, "Touched"),
            "keyUp" => {
                let context = e.context.clone();
                c.background(
                    &e.context,
                    |cancel| {
                        if cancel.load(std::sync::atomic::Ordering::Acquire) {
                            return Err(std::io::Error::other("cancelled").into());
                        }
                        Ok(json!("Background result"))
                    },
                    move |client, value| client.title(&context, value.as_str().unwrap_or("")),
                )?;
                Ok(())
            }
            _ => Ok(()),
        }
    }
    // Return the next deadline that requires owner-loop attention
    fn next_poll(&self, _now: std::time::Instant) -> Option<std::time::Instant> {
        None
    }
    // Handle a compatible batch of dial input
    fn batch_dial(&self, _action: &str) -> bool {
        true
    }
}
// Run the advanced action lifecycle example
fn main() -> Result<()> {
    stream_dock_manager_plugin_sdk::run(&mut Advanced::default(), std::env::args().skip(1), None)
}
