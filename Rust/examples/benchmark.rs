use stream_dock_manager_plugin_sdk::{
    authoring::{Display, Renderer},
    testkit::Recorder,
    HostInfo,
};
// Run the bounded SDK throughput benchmark
fn main() {
    let args: Vec<_> = std::env::args().skip(1).collect();
    let get = |name: &str, default: usize| {
        args.windows(2)
            .find(|pair| pair[0] == name)
            .map(|pair| pair[1].parse().unwrap())
            .unwrap_or(default)
    };
    let frames = get("--frames", 120);
    let surfaces = get("--surfaces", 14);
    assert!((1..=1200).contains(&frames) && (1..=14).contains(&surfaces));
    let mut recorder = Recorder::new(HostInfo::default());
    let mut renderer = Renderer::default();
    let started = std::time::Instant::now();
    let mut count = 0;
    for frame in 0..frames {
        for surface in 0..surfaces {
            let context = format!("fixture-{surface}");
            let view = Display {
                title: frame.to_string(),
                ..Default::default()
            };
            for _ in 0..2 {
                renderer
                    .render(&recorder.client, &context, view.clone())
                    .unwrap();
            }
            let events = recorder.drain();
            assert_eq!(events.len(), if frame == 0 { 2 } else { 1 });
            count += events.len();
        }
    }
    println!(
        "{}",
        serde_json::json!({"schema_version":1,"measurement_scope":"sdk_authoring_inprocess","implementation":"rust","frames":frames,"surfaces":surfaces,"received_events":count,"display_revisions":frames*surfaces,"elapsed_ms":started.elapsed().as_secs_f64()*1000.})
    );
}
