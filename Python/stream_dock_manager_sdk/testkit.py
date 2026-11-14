"""Real handlers and capability checks, without sockets, Manager or wall-clock sleeps."""
from .protocol import Event, HostInfo
from .runtime import Client


class Clock:
    # Initialize the deterministic fixture clock
    def __init__(self, now=0): self.value = now
    # Read the clock used by this scheduler or fixture
    def now(self): return self.value
    # Advance the fixture clock by the requested interval
    def advance(self, seconds):
        if seconds < 0: raise ValueError('clock cannot go backwards')
        self.value += seconds


class Recorder:
    # Bind the fake client and initialize recorded message history
    def __init__(self, host=None):
        self.client, self.history = Client(host or HostInfo(), 'test-token'), []

    # Drain recorder data
    def drain(self):
        messages = self.client.drain()
        self.history.extend(messages)
        return messages

    # Replay recorded host events through the supplied handler
    def replay(self, handler, events):
        for event in events: self.client._dispatch(handler, Event.parse(event))

    # Poll recorder for ready work or updated state
    def poll(self, handler):
        self.client._drain_results()
        handler.poll(self.client)

    # Stop recorder activity and release owned resources
    def close(self): self.client.close()

    # Assert that the recorded output contains the expected message
    def expect(self, expected):
        actual = self.drain()
        if actual != expected: raise AssertionError(f'recorded events differ: {actual!r} != {expected!r}')

    # Assert contexts in the test fixture
    def assert_contexts(self, *allowed):
        for message in self.history:
            if message.get('context', '') not in allowed: raise AssertionError('event escaped allowed contexts: ' + message['event'])


# Trust only the public loopback test certificate; never use for real services
def tls():
    """Trust only the public loopback test certificate; never use for real services."""
    import ssl
    from importlib.resources import files
    root=files('stream_dock_manager_sdk').joinpath('testdata/tls')
    server=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    server.load_cert_chain(str(root.joinpath('localhost-cert.pem')),str(root.joinpath('localhost-key.pem')))
    client=ssl.create_default_context(cadata=root.joinpath('localhost-cert.pem').read_text())
    return server,client


class Peer:
    # Create a disposable loopback listener for transport fixtures
    def __init__(self,handler,context=None):
        import socket
        import threading
        self.listener=socket.socket(); self.listener.bind(('127.0.0.1',0)); self.listener.listen(32);self.listener.settimeout(.05)
        self.address=self.listener.getsockname(); self.stop=threading.Event(); self.connections=[];self.workers=[]
        # Serve the disposable fixture connection
        def serve():
            while not self.stop.is_set():
                try: connection,_=self.listener.accept()
                except (TimeoutError,OSError): continue
                connection.settimeout(2)
                self.connections.append(connection)
                # Drive the peer execution loop
                def run(connection=connection):
                    try:
                        if context: connection=context.wrap_socket(connection,server_side=True)
                        handler(connection)
                    finally: connection.close()
                worker=threading.Thread(target=run,daemon=True);self.workers.append(worker);worker.start()
        self.worker=threading.Thread(target=serve,daemon=True);self.worker.start()
    # Stop peer activity and release owned resources
    def close(self):
        import socket
        self.stop.set();self.listener.close()
        for connection in self.connections:
            try: connection.shutdown(socket.SHUT_RDWR)
            except OSError: pass
            connection.close()
        self.worker.join(2)
        for worker in self.workers: worker.join(2)
    # Enter the managed peer scope
    def __enter__(self): return self
    # Release peer resources when leaving the scope
    def __exit__(self,*args): self.close()
