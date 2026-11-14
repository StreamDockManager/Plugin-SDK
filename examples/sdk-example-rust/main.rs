// SPDX-License-Identifier: MIT
use serde_json::{json, Value};
use stream_dock_manager_plugin_sdk::{
    authoring::{Action, Binding, Display, Plugin},
    Client, Event, Handler, PortableSettings, Result,
};

// Normalize counter values while preserving unrelated saved settings
fn clean(value: &Value) -> Value {
    let mut settings = value.as_object().cloned().unwrap_or_default();
    let prefix = settings
        .get("prefix")
        .and_then(Value::as_str)
        .unwrap_or("Count")
        .chars()
        .take(80)
        .collect::<String>();
    let count = settings
        .get("count")
        .and_then(Value::as_u64)
        .filter(|v| *v < 1u64 << 53)
        .unwrap_or(0);
    settings.insert("prefix".into(), json!(prefix));
    settings.insert("count".into(), json!(count));
    Value::Object(settings)
}
struct CounterAction;
impl Action for CounterAction {
    // Normalize settings restored when the counter binding appears
    fn appeared(&mut self, plugin: &mut Plugin, _: &Client, event: &Event) -> Result<()> {
        let binding = plugin.bindings.get_mut(&event.context).unwrap();
        binding.settings = clean(&binding.settings);
        Ok(())
    }
    // Reuse appearance normalization for later host settings updates
    fn changed(&mut self, plugin: &mut Plugin, client: &Client, event: &Event) -> Result<()> {
        self.appeared(plugin, client, event)
    }
    // Apply key presses and inspector changes through portable host settings
    fn handle(&mut self, plugin: &mut Plugin, client: &Client, event: &Event) -> Result<()> {
        let Some(binding) = plugin.bindings.get_mut(&event.context) else {
            return Ok(());
        };
        let mut settings = clean(&binding.settings);
        if event.event == "keyUp" {
            settings["count"] = json!(settings["count"].as_u64().unwrap() + 1)
        } else if event.event == "sendToPlugin" {
            if event.payload["reset"] == true {
                settings["count"] = json!(0)
            } else if event.payload.get("settings").is_some() {
                settings["prefix"] = clean(&event.payload["settings"])["prefix"].clone()
            } else if event.payload["request"] != "settings" {
                return Ok(());
            }
        } else {
            return Ok(());
        }
        binding.settings = settings.clone();
        if event.event == "keyUp" || event.payload["request"] != "settings" {
            PortableSettings::new(client.clone(), "action", &event.context)?
                .replace(settings.as_object().cloned().unwrap_or_default())?;
        };
        client.to_inspector(&event.context, &event.action, json!({"settings":settings}))
    }
    // Render the count as a title, parity image and layout feedback
    fn render(&mut self, _: &mut Plugin, _: &str, binding: &Binding) -> Result<Option<Display>> {
        let settings = clean(&binding.settings);
        let count = settings["count"].as_u64().unwrap();
        let name = if count % 2 == 0 { "even" } else { "odd" };
        let mut feedback = std::collections::BTreeMap::new();
        feedback.insert("prefix".into(), settings["prefix"].clone());
        feedback.insert("count".into(), json!(count));
        feedback.insert("icon".into(), json!(format!("icons/{name}.svg")));
        // Fixed trusted SVGs encoded inline; no dependency update or file reads
        // New SDK consumers can encode dynamic bytes with image_data_uri
        let image = if count % 2 == 0 {
            "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCAzMiAzMiI+PGcgZmlsbD0ibm9uZSIgc3Ryb2tlPSIjZmZmZmZmIiBzdHJva2Utd2lkdGg9IjMiIHN0cm9rZS1saW5lY2FwPSJyb3VuZCI+PHBhdGggZD0iTTcgOWgxOE03IDE5aDE4Ii8+PC9nPjwvc3ZnPgo="
        } else {
            "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCAzMiAzMiI+PGcgZmlsbD0ibm9uZSIgc3Ryb2tlPSIjZmZmZmZmIiBzdHJva2Utd2lkdGg9IjMiIHN0cm9rZS1saW5lY2FwPSJyb3VuZCI+PHBhdGggZD0iTTE2IDZ2MjBNNiAxNmgyMCIvPjwvZz48L3N2Zz4K"
        }.to_owned();
        Ok(Some(Display {
            title: format!("{} {count}", settings["prefix"].as_str().unwrap()),
            image,
            state: (count % 2) as u32,
            layout: "display/counter.json".into(),
            feedback,
        }))
    }
}
#[derive(Default)]
struct Counter {
    plugin: Plugin,
}
impl Handler for Counter {
    // Log completed key presses rather than every lifecycle event
    fn should_log_action(&self, event: &Event) -> bool {
        event.event == "keyUp"
    }
    // Register new action IDs and refresh the display after inspector input
    fn handle(&mut self, client: &Client, event: &Event) -> Result<()> {
        if !event.action.is_empty() && !self.plugin.is_registered(&event.action) {
            self.plugin.register(&event.action, CounterAction)?
        }
        self.plugin.handle(client, event)?;
        if event.event == "sendToPlugin" {
            self.plugin.render(client, &event.context)?
        };
        Ok(())
    }
    // Delegate deferred work to the authoring plugin
    fn poll(&mut self, client: &Client) -> Result<()> {
        self.plugin.poll(client)
    }
    // Expose the authoring plugin deadline to the host event loop
    fn next_poll(&self, now: std::time::Instant) -> Option<std::time::Instant> {
        self.plugin.next_poll(now)
    }
    // Release authoring resources when the host connection closes
    fn close(&mut self) -> Result<()> {
        self.plugin.close()
    }
}
// Load the embedded capability descriptor and run the counter host connection
fn main() {
    let descriptor =
        stream_dock_manager_plugin_sdk::embed_capabilities!(include_str!("descriptor.txt"));
    if let Err(error) = descriptor.and_then(|cap| {
        stream_dock_manager_plugin_sdk::run(
            &mut Counter::default(),
            std::env::args().skip(1),
            Some(cap),
        )
    }) {
        eprintln!("{error}");
        std::process::exit(1)
    }
}
