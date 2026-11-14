use serde_json::{json, Value};
use std::{
    net::{TcpListener, TcpStream},
    sync::{atomic::AtomicBool, Arc},
    thread,
    time::{Duration, Instant},
};
use stream_dock_manager_plugin_sdk::*;
use tungstenite::{accept, Message, WebSocket};

// Read the negotiated host descriptor
fn host(
    script: impl FnOnce(WebSocket<TcpStream>) + Send + 'static,
) -> (u16, thread::JoinHandle<()>) {
    let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
    let port = listener.local_addr().unwrap().port();
    let worker = thread::spawn(move || {
        let (stream, _) = listener.accept().unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(2)))
            .unwrap();
        let mut socket = accept(stream).unwrap();
        let registration: Value =
            serde_json::from_str(socket.read().unwrap().to_text().unwrap()).unwrap();
        assert_eq!(
            registration,
            json!({"event":"registerPlugin","uuid":"fixture"})
        );
        script(socket);
    });
    (port, worker)
}
// Verify options
fn options(port: u16) -> LaunchOptions {
    LaunchOptions {
        port,
        identity: "fixture".into(),
        host: HostInfo::default(),
    }
}
// Queue the outbound message for the owned connection
fn send(socket: &mut WebSocket<TcpStream>, event: Value) {
    socket
        .send(Message::Text(event.to_string().into()))
        .unwrap();
}

#[test]
// Verify storage replies are correlated while handler waits without dispatching controls
fn storage_replies_are_correlated_while_handler_waits_without_dispatching_controls() {
    let (port, worker) = host(|mut socket| {
        send(&mut socket, json!({"event":"keyUp","context":"owned"}));
        let request: Value =
            serde_json::from_str(socket.read().unwrap().to_text().unwrap()).unwrap();
        assert_eq!(request["event"], RPC_REQUEST_EVENT);
        assert_eq!(request["method"], "get");
        // This ordinary notification remains queued while the callback awaits its reply
        send(
            &mut socket,
            json!({"event":"didReceiveSettings","context":"owned","payload":{"settings":{"unrelated":true}}}),
        );
        send(
            &mut socket,
            json!({"event":RPC_RESPONSE_EVENT,"protocol_id":STORAGE_PROTOCOL,"request_id":request["request_id"],"result":{"generation":"1","revision":"0","found":false}}),
        );
        let request: Value =
            serde_json::from_str(socket.read().unwrap().to_text().unwrap()).unwrap();
        // Raw envelope bytes include whitespace; re-encoding must not evade the limit
        let response = json!({"event":RPC_RESPONSE_EVENT,"protocol_id":STORAGE_PROTOCOL,"request_id":request["request_id"],"result":{"generation":"1","revision":"0","found":false}}).to_string();
        socket
            .send(Message::Text(
                format!("{}{}", " ".repeat(MAXIMUM_STORAGE_MESSAGE), response).into(),
            ))
            .unwrap();
        let request: Value =
            serde_json::from_str(socket.read().unwrap().to_text().unwrap()).unwrap();
        assert_eq!(request["event"], "getSettings");
        send(
            &mut socket,
            json!({"event":"didReceiveSettings","context":"owned","payload":"invalid"}),
        );
        let title: Value = serde_json::from_str(socket.read().unwrap().to_text().unwrap()).unwrap();
        assert_eq!(title["event"], "setTitle");
        send(&mut socket, json!({"event":"shutdown"}));
        let _ = socket.read();
    });
    struct Awaiting {
        owner: thread::ThreadId,
        events: Vec<String>,
    }
    impl Handler for Awaiting {
        // Dispatch the received event to the registered behavior
        fn handle(&mut self, client: &Client, event: &Event) -> Result<()> {
            assert_eq!(thread::current().id(), self.owner);
            self.events.push(event.event.clone());
            if event.event == "keyUp" {
                let value = HostStore::new(client.clone(), "authoring")?.get("missing")?;
                assert!(!value.found);
                let error = HostStore::new(client.clone(), "authoring")?
                    .get("oversized")
                    .unwrap_err();
                assert_eq!(
                    error.downcast_ref::<RpcError>().unwrap().code,
                    "invalid_response"
                );
                let error = PortableSettings::new(client.clone(), "action", &event.context)?
                    .read()
                    .unwrap_err();
                assert_eq!(
                    error.downcast_ref::<RpcError>().unwrap().code,
                    "invalid_response"
                );
                client.title(&event.context, "ready")?;
            }
            Ok(())
        }
        // Return the next deadline that requires owner-loop attention
        fn next_poll(&self, _: Instant) -> Option<Instant> {
            None
        }
    }
    let mut handler = Awaiting {
        owner: thread::current().id(),
        events: vec![],
    };
    let mut launch = options(port);
    launch.host = HostInfo::parse(
        &json!({"streamDockManager":{"contract_version":1,"capabilities":[STORAGE_PROTOCOL]}})
            .to_string(),
    )
    .unwrap();
    connect(&mut handler, launch, None, Arc::new(AtomicBool::new(false))).unwrap();
    worker.join().unwrap();
    assert_eq!(
        handler.events,
        ["keyUp", "didReceiveSettings", "didReceiveSettings"]
    );
}

#[test]
// Verify incoming flood preserves fifo release and shutdown
fn incoming_flood_preserves_fifo_release_and_shutdown() {
    let (port, worker) = host(|mut socket| {
        let text = "é".repeat(300_000);
        for index in 0..24 {
            send(
                &mut socket,
                json!({"event":"keyDown", "payload":{"index":index,"text":text}}),
            );
        }
        send(&mut socket, json!({"event":"keyUp"}));
        send(&mut socket, json!({"event":"shutdown"}));
        let _ = socket.read();
    });
    #[derive(Default)]
    struct Flood {
        events: Vec<(String, Value)>,
        closed: usize,
    }
    impl Handler for Flood {
        // Dispatch the received event to the registered behavior
        fn handle(&mut self, _: &Client, event: &Event) -> Result<()> {
            if self.events.is_empty() {
                thread::sleep(Duration::from_millis(30));
            }
            self.events
                .push((event.event.clone(), event.payload["index"].clone()));
            Ok(())
        }
        // Return the next deadline that requires owner-loop attention
        fn next_poll(&self, _: Instant) -> Option<Instant> {
            None
        }
        // Close the owned session and release pending work
        fn close(&mut self) -> Result<()> {
            self.closed += 1;
            Ok(())
        }
    }
    let mut handler = Flood::default();
    connect(
        &mut handler,
        options(port),
        None,
        Arc::new(AtomicBool::new(false)),
    )
    .unwrap();
    worker.join().unwrap();
    assert_eq!(handler.closed, 1);
    assert_eq!(handler.events.len(), 25);
    for (index, event) in handler.events[..24].iter().enumerate() {
        assert_eq!(event, &("keyDown".into(), json!(index)));
    }
    assert_eq!(handler.events[24].0, "keyUp");
}

#[test]
// Verify batches only compatible dials and flushes before shutdown
fn batches_only_compatible_dials_and_flushes_before_shutdown() {
    let (port, worker) = host(|mut socket| {
        // Deliver one burst. Flushing each tiny frame separately can put them
        // outside the 16 ms batching window on platforms with delayed TCP ACKs
        for (context, ticks) in [("a", 2), ("a", 3), ("a", -1), ("b", -4)] {
            socket.write(Message::Text(
                json!({"event":"dialRotate","action":"dial","context":context,"payload":{"ticks":ticks}})
                    .to_string().into(),
            )).unwrap();
        }
        socket
            .write(Message::Text(
                json!({"event":"shutdown"}).to_string().into(),
            ))
            .unwrap();
        socket.flush().unwrap();
        // Keep the server alive through the client's orderly shutdown
        let _ = socket.read();
    });
    #[derive(Default)]
    struct Dials {
        values: Vec<(String, i64)>,
        closed: bool,
    }
    impl Handler for Dials {
        // Dispatch the received event to the registered behavior
        fn handle(&mut self, _: &Client, event: &Event) -> Result<()> {
            self.values.push((
                event.context.clone(),
                event.payload["ticks"].as_i64().unwrap(),
            ));
            Ok(())
        }
        // Handle a compatible batch of dial input
        fn batch_dial(&self, _: &str) -> bool {
            true
        }
        // Return the next deadline that requires owner-loop attention
        fn next_poll(&self, _: Instant) -> Option<Instant> {
            None
        }
        // Close the owned session and release pending work
        fn close(&mut self) -> Result<()> {
            self.closed = true;
            Ok(())
        }
    }
    let mut handler = Dials::default();
    connect(
        &mut handler,
        options(port),
        None,
        Arc::new(AtomicBool::new(false)),
    )
    .unwrap();
    worker.join().unwrap();
    assert_eq!(
        handler.values,
        [("a".into(), 5), ("a".into(), -1), ("b".into(), -4)]
    );
    assert!(handler.closed);
}

#[test]
// Verify scheduled polls wait for deadlines and run on the handler thread
fn scheduled_polls_wait_for_deadlines_and_run_on_the_handler_thread() {
    let (port, worker) = host(|mut socket| {
        let _ = socket.read();
    });
    struct Scheduled {
        calls: Vec<Instant>,
        owner: thread::ThreadId,
        closed: bool,
    }
    impl Handler for Scheduled {
        // Dispatch the received event to the registered behavior
        fn handle(&mut self, _: &Client, _: &Event) -> Result<()> {
            Ok(())
        }
        // Return the next deadline that requires owner-loop attention
        fn next_poll(&self, now: Instant) -> Option<Instant> {
            Some(now + Duration::from_millis(30))
        }
        // Advance scheduled work on the owning event loop
        fn poll(&mut self, client: &Client) -> Result<()> {
            assert_eq!(self.owner, thread::current().id());
            self.calls.push(Instant::now());
            if self.calls.len() == 3 {
                client.stop()
            }
            Ok(())
        }
        // Close the owned session and release pending work
        fn close(&mut self) -> Result<()> {
            self.closed = true;
            Ok(())
        }
    }
    let mut handler = Scheduled {
        calls: vec![],
        owner: thread::current().id(),
        closed: false,
    };
    connect(
        &mut handler,
        options(port),
        None,
        Arc::new(AtomicBool::new(false)),
    )
    .unwrap();
    worker.join().unwrap();
    assert_eq!(handler.calls.len(), 3);
    assert!(handler
        .calls
        .windows(2)
        .all(|pair| pair[1].duration_since(pair[0]) >= Duration::from_millis(25)));
    assert!(handler.closed);
}

#[test]
// Verify malformed messages close handler and cancel session
fn malformed_messages_close_handler_and_cancel_session() {
    for raw in [
        "[]",
        "{}",
        "{\"event\":1}",
        "{\"event\":\"keyUp\",\"payload\":[]}",
    ] {
        let (port, worker) = host(move |mut socket| {
            socket.send(Message::Text(raw.into())).unwrap();
            let _ = socket.read();
        });
        #[derive(Default)]
        struct Closed {
            closed: bool,
            client: Option<Client>,
        }
        impl Handler for Closed {
            // Dispatch the received event to the registered behavior
            fn handle(&mut self, _: &Client, _: &Event) -> Result<()> {
                panic!("invalid event dispatched")
            }
            // Return the next deadline that requires owner-loop attention
            fn next_poll(&self, now: Instant) -> Option<Instant> {
                Some(now)
            }
            // Advance scheduled work on the owning event loop
            fn poll(&mut self, client: &Client) -> Result<()> {
                self.client = Some(client.clone());
                Ok(())
            }
            // Close the owned session and release pending work
            fn close(&mut self) -> Result<()> {
                self.closed = true;
                Ok(())
            }
        }
        let mut handler = Closed::default();
        assert!(connect(
            &mut handler,
            options(port),
            None,
            Arc::new(AtomicBool::new(false))
        )
        .is_err());
        worker.join().unwrap();
        assert!(handler.closed);
        assert!(handler.client.unwrap().title("binding", "late").is_err());
    }
}

#[test]
// Verify no deadline wait wakes for worker completion
fn no_deadline_wait_wakes_for_worker_completion() {
    let (port, worker) = host(|mut socket| {
        send(&mut socket, json!({"event":"keyDown","context":"a"}));
        loop {
            let message = socket.read().unwrap();
            if message.is_close() {
                break;
            }
            if message.is_text() {
                let value: Value = serde_json::from_str(message.to_text().unwrap()).unwrap();
                if value["event"] == "setTitle" && value["payload"]["title"] == "worker:42" {
                    send(&mut socket, json!({"event":"shutdown"}));
                    let _ = socket.read();
                    break;
                }
            }
        }
    });
    struct Background;
    impl Handler for Background {
        // Return the next deadline that requires owner-loop attention
        fn next_poll(&self, _: Instant) -> Option<Instant> {
            None
        }
        // Dispatch the received event to the registered behavior
        fn handle(&mut self, client: &Client, _: &Event) -> Result<()> {
            client.background(
                "a",
                |_| {
                    thread::sleep(Duration::from_millis(30));
                    Ok(json!(42))
                },
                |client, result| client.title("a", &format!("worker:{result}")),
            )?;
            Ok(())
        }
    }
    connect(
        &mut Background,
        options(port),
        None,
        Arc::new(AtomicBool::new(false)),
    )
    .unwrap();
    worker.join().unwrap();
}

#[test]
// Verify base handler has no implicit poll deadline
fn base_handler_has_no_implicit_poll_deadline() {
    struct EventOnly;
    impl Handler for EventOnly {
        // Dispatch the received event to the registered behavior
        fn handle(&mut self, _: &Client, _: &Event) -> Result<()> {
            Ok(())
        }
    }
    assert_eq!(EventOnly.next_poll(Instant::now()), None);
}
