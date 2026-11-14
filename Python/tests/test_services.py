import json
from pathlib import Path
import sys
import tempfile
import threading
import time
import unittest
from contextlib import contextmanager
from stream_dock_manager_sdk import connectors, credentials, buildcache, process
from stream_dock_manager_sdk.testkit import Recorder

FIXTURE = json.loads((Path(__file__).parents[2] / 'contracts/fixtures/services-v1.json').read_text())


# Prepare service peer in the services fixture
@contextmanager
def service_peer(handler, secure):
    import http.client
    from unittest.mock import patch
    from stream_dock_manager_sdk.testkit import Peer, tls
    if secure:
        server, client = tls()
        initialize = http.client.HTTPSConnection.__init__
        # Check that the service destination meets the configured trust policy
        def trusted(connection, *args, **kwargs):
            initialize(connection, *args, context=client, **kwargs)
        # Keep the class binding used by http.client's explicit super() calls
        with patch.object(http.client.HTTPSConnection, '__init__', trusted):
            with Peer(handler, server) as peer:
                # Match the IPv4 listener; its certificate includes this IP SAN
                yield f'https://{peer.address[0]}:{peer.address[1]}/'
    else:
        with Peer(handler) as peer:
            yield f'http://127.0.0.1:{peer.address[1]}/'


class Services(unittest.TestCase):
    # Verify shared cache artifacts
    def test_shared_cache_artifacts(self):
        import unittest.mock
        pattern = bytes(FIXTURE['cache_artifacts']['byte_pattern'])
        chunk = pattern * ((64 << 10) // len(pattern))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/'artifact'
            for case in FIXTURE['cache_artifacts']['cases']:
                with self.subTest(size=case['bytes']):
                    with path.open('wb') as stream:
                        remaining = case['bytes']
                        while remaining:
                            count = min(remaining, len(chunk)); stream.write(chunk[:count]); remaining -= count
                    # Hashing must not regress to a whole-artifact read
                    with unittest.mock.patch.object(Path, 'read_bytes', side_effect=AssertionError('whole-file read')):
                        buildcache.remember(path, 'fixture')
                        self.assertTrue(buildcache.cached(path, 'fixture'))
                        self.assertFalse(buildcache.cached(path, 'different'))
                    record = json.loads(Path(str(path)+'.build.json').read_text())
                    self.assertEqual(record['output'], case['sha256'])
                    with path.open('r+b') as stream:
                        stream.seek(max(0, case['bytes']-1)); stream.write(b'c')
                    self.assertFalse(buildcache.cached(path, 'fixture'))
                    path.unlink()
                    self.assertFalse(buildcache.cached(path, 'fixture'))
                    self.assertRaises(OSError, buildcache.remember, path, 'fixture')

    # Verify shared credentials and cache
    def test_shared_credentials_and_cache(self):
        from stream_dock_manager_sdk import settings
        for case in FIXTURE['typed_values']:
            self.assertEqual(settings.number(case['input'], 'n', 99), case['number'])
            self.assertEqual(settings.string(case['input'], 's', 'fallback'), case['string'])
            self.assertEqual(settings.boolean(case['input'], 'b'), case['boolean'])
        for case in FIXTURE['credentials']:
            call = lambda: credentials.secret(case['settings'], case['key'], case['lookup'].get)
            if case.get('invalid'): self.assertRaises(ValueError, call)
            else: self.assertEqual(call(), case['value'])
        for case in FIXTURE['fingerprints']: self.assertEqual(buildcache.fingerprint(case['inputs'], case['configuration']), case['expected'])
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/'artifact'; path.write_bytes(b'original'); buildcache.remember(path, 'key'); self.assertTrue(buildcache.cached(path, 'key')); path.write_bytes(b'corrupt'); self.assertFalse(buildcache.cached(path, 'key'))
    # Verify bounded process arguments and cancellation
    def test_bounded_process_arguments_and_cancellation(self):
        value = process.run(sys.executable, ['-c', 'import json,sys; print(json.dumps(sys.argv[1:]))', *FIXTURE['arguments']])
        self.assertEqual(json.loads(value), FIXTURE['arguments'])
        self.assertRaises(ValueError, process.run, sys.executable, ['-c', 'print("x"*2000)'], output_limit=10)
        cancel=threading.Event(); cancel.set(); self.assertRaises(RuntimeError, process.run, sys.executable, ['-c','pass'], cancel=cancel)
    # Reject overflow before the child exits or fills a complete pipe read
    def test_bounded_process_detects_output_before_exit(self):
        for stream in ('stdout', 'stderr'):
            with self.subTest(stream=stream):
                with self.assertRaisesRegex(ValueError, 'process response exceeds limit'):
                    process.run(sys.executable, ['-c', f'import sys,time; print("x" * 2000, file=sys.{stream}, flush=True); time.sleep(30)'],
                                timeout=2, output_limit=10)

    # Verify shared read and read only inspector
    def test_shared_read_and_read_only_inspector(self):
        class Driver:
            reads = 0
            commands = 0
            # Return key for driver
            def key(self, settings): return 'one'
            # Read the next driver value or data segment
            def read(self, cancel, settings): self.reads += 1; return 'sample'
            # Prepare command in the driver fixture
            def command(self, *args): self.commands += 1; return connectors.Outcome('delivered')
            # Return discover for driver
            def discover(self, *args): return []
            # Return display for driver
            def display(self, value, *args): return value
            # Stop driver activity and release owned resources
            def close(self): pass
        driver=Driver(); plugin=connectors.Connector(driver, connectors.Options(('status',), 10)); recorder=Recorder()
        for context in ('a','b'): recorder.replay(plugin,[dict(event='willAppear',context=context,action='status',payload=dict(settings={}))])
        # Drain services data
        def drain():
            deadline=time.monotonic()+2
            while True:
                recorder.poll(plugin)
                if not plugin.busy: return
                if time.monotonic()>=deadline: self.fail('connector work did not drain')
                time.sleep(.005)
        drain(); self.assertEqual(driver.reads,1)
        recorder.replay(plugin,[dict(event='sendToPlugin',context='inspector',action='status',payload=dict(op='discover',request_id='picker-1'))]); drain(); self.assertEqual(driver.commands,0)
        self.assertTrue(any(e['event']=='sendToPropertyInspector' and e['payload'].get('request_id')=='picker-1' and e['payload'].get('available') for e in recorder.drain()))
        for context in ('a','b'): recorder.replay(plugin,[dict(event='willDisappear',context=context,action='status')])
        self.assertIsNone(plugin.next_poll(time.monotonic())); plugin.close(); recorder.close()

    # Verify service json retry and tls peer
    def test_service_json_retry_and_tls_peer(self):
        import socket
        from stream_dock_manager_sdk.service import Service,HTTPError
        from stream_dock_manager_sdk.testkit import Peer,tls
        # Process the event in the services callback
        def handler(connection):
            request=b''
            while not request.endswith(b'\r\n\r\n'): request+=connection.recv(1)
            if b'/fail ' in request: connection.sendall(b'HTTP/1.1 429 Too Many Requests\r\nRetry-After: 2\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
            else: connection.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 12\r\nConnection: close\r\n\r\n{"value":42}')
        with Peer(handler) as peer:
            service=Service()
            try:
                self.assertEqual(service.json('GET',f'http://127.0.0.1:{peer.address[1]}/'),{'value':42})
                with self.assertRaises(HTTPError) as failure: service.json('GET',f'http://127.0.0.1:{peer.address[1]}/fail')
                self.assertEqual(failure.exception.status,429);self.assertGreater(failure.exception.retry_at,time.time())
            finally: service.close()
        server,client=tls()
        with Peer(lambda connection: connection.sendall(connection.recv(4)),server) as peer:
            with client.wrap_socket(socket.create_connection(peer.address),server_hostname='localhost') as connection:
                connection.sendall(b'ping');self.assertEqual(connection.recv(4),b'ping')

    # Verify connector commands are serialized and not replayed
    def test_connector_commands_are_serialized_and_not_replayed(self):
        class Driver:
            commands=[]
            active=peak=0
            # Return key for driver
            def key(self, settings): return 'one'
            # Read the next driver value or data segment
            def read(self, cancel, settings): return 'sample'
            # Prepare command in the driver fixture
            def command(self, cancel, action, settings, ticks):
                self.active+=1; self.peak=max(self.peak,self.active)
                try:
                    self.commands.append(ticks)
                    cancel.wait(.005)
                    return connectors.Outcome('delivered')
                finally:self.active-=1
            # Return discover for driver
            def discover(self,*args):return []
            # Return display for driver
            def display(self,value,*args):return value
            # Stop driver activity and release owned resources
            def close(self):pass
        driver=Driver();plugin=connectors.Connector(driver,connectors.Options(('volume',),10,dial_actions=('volume',)));recorder=Recorder()
        recorder.replay(plugin,[dict(event='willAppear',context='a',action='volume',payload=dict(settings={}))])
        for tick in (2,3,-1,4):recorder.replay(plugin,[dict(event='dialRotate',context='a',action='volume',payload=dict(ticks=tick))])
        deadline=time.monotonic()+2
        while plugin.busy or plugin.queue:
            recorder.poll(plugin)
            if time.monotonic()>deadline:self.fail('commands did not complete')
            time.sleep(.002)
        for _ in range(5):recorder.poll(plugin)
        self.assertEqual(driver.commands,[2,3,-1,4]);self.assertEqual(driver.peak,1)
        recorder.replay(plugin,[dict(event='willDisappear',context='a',action='volume')]);plugin.close();recorder.close()

    # Verify inspector hook preserves action fallback
    def test_inspector_hook_preserves_action_fallback(self):
        from stream_dock_manager_sdk.authoring import Plugin,Action
        calls=[];plugin=Plugin();recorder=Recorder()
        plugin.register('action',Action(handle=lambda p,c,e:calls.append('action')))
        recorder.replay(plugin,[dict(event='willAppear',context='a',action='action',payload=dict(settings={}))])
        event=dict(event='sendToPlugin',context='a',action='action',payload={})
        recorder.replay(plugin,[event]);self.assertEqual(calls,['action'])
        plugin.inspector=lambda p,c,e:calls.append('inspector')
        recorder.replay(plugin,[event]);self.assertEqual(calls,['action','inspector'])
        plugin.inspector=None;recorder.replay(plugin,[event]);self.assertEqual(calls,['action','inspector','action'])
        plugin.close();recorder.close()

    # Verify service slow origin does not block independent origin
    def test_service_slow_origin_does_not_block_independent_origin(self):
        from stream_dock_manager_sdk.service import Service
        from stream_dock_manager_sdk.testkit import Peer
        entered, release = threading.Event(), threading.Event()
        # Process the event in the services callback
        def handler(connection, slow=False):
            request = b''
            while not request.endswith(b'\r\n\r\n'): request += connection.recv(1)
            if slow:
                entered.set()
                release.wait(2)
            connection.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}')
        with Peer(lambda c: handler(c, True)) as slow, Peer(handler) as fast:
            service = Service(timeout=3)
            errors = []
            # Drive the services execution loop
            def run(endpoint):
                try: service.json('GET', endpoint)
                except Exception as error: errors.append(error)
            blocked = threading.Thread(target=run, args=(f'http://127.0.0.1:{slow.address[1]}/',))
            quick = threading.Thread(target=run, args=(f'http://127.0.0.1:{fast.address[1]}/',))
            try:
                blocked.start()
                self.assertTrue(entered.wait(1))
                quick.start()
                quick.join(.5)
                self.assertFalse(quick.is_alive(), 'unrelated origin blocked by service-wide network lock')
            finally:
                release.set()
                blocked.join(3)
                quick.join(3)
                service.close()
            self.assertEqual(errors, [])

    # Verify service waiting lease cancellation and concurrent close
    def test_service_waiting_lease_cancellation_and_concurrent_close(self):
        from stream_dock_manager_sdk.service import Service
        from stream_dock_manager_sdk.testkit import Peer
        entered, release = threading.Event(), threading.Event()
        # Process the event in the services callback
        def handler(connection):
            data = b''
            while not data.endswith(b'\r\n\r\n'): data += connection.recv(1)
            entered.set()
            release.wait(2)
            try: connection.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}')
            except OSError: pass
        with Peer(handler) as peer:
            service = Service(timeout=3)
            endpoint = f'http://127.0.0.1:{peer.address[1]}/'
            cancel = threading.Event()
            errors = []
            # Submit the services request and wait for its response
            def request(cancellation=None):
                try: service.do('GET', endpoint, cancel=cancellation)
                except Exception as error: errors.append(str(error))
            first = threading.Thread(target=request)
            waiting = threading.Thread(target=request, args=(cancel,))
            try:
                first.start()
                self.assertTrue(entered.wait(1))
                waiting.start()
                cancel.set()
                waiting.join(1)
                self.assertFalse(waiting.is_alive())
                self.assertTrue(any('cancel' in error for error in errors))
                service.close()
                first.join(1)
                self.assertFalse(first.is_alive(), 'close did not wake the in-flight response read')
                with self.assertRaisesRegex(RuntimeError, 'closed'): service.do('GET', endpoint)
            finally:
                release.set()
                first.join(3)
                waiting.join(3)
                service.close()

    # Verify service pool saturation waits and only evicts idle leases
    def test_service_pool_saturation_waits_and_only_evicts_idle_leases(self):
        from stream_dock_manager_sdk.service import Service
        from unittest.mock import patch
        import queue
        created, entered = [], queue.Queue()
        lock = threading.Lock()
        class Connection:
            alive = peak = 0
            # Initialize connection state and dependencies
            def __init__(self, host, port, timeout):
                self.host, self.sock, self.timeout = host, None, timeout
                self.release, self.closed = threading.Event(), False
                with lock:
                    created.append(self)
                    Connection.alive += 1
                    Connection.peak = max(Connection.peak, Connection.alive)
            # Submit the connection request and wait for its response
            def request(self, *args, **kwargs): entered.put(self)
            # Return getresponse for connection
            def getresponse(self): return Response(self)
            # Stop connection activity and release owned resources
            def close(self):
                with lock:
                    if not self.closed:
                        self.closed = True
                        Connection.alive -= 1
                self.release.set()
        class Response:
            status = 200
            # Initialize response state and dependencies
            def __init__(self, connection): self.connection, self.sent = connection, False
            # Enter the managed response scope
            def __enter__(self): return self
            # Release response resources when leaving the scope
            def __exit__(self, *args): pass
            # Read the next response value or data segment
            def read(self, count):
                if not self.connection.release.wait(5): raise TimeoutError('fixture was not released')
                if self.sent: return b''
                self.sent = True
                return b'{}'
        errors = []
        service = Service(timeout=5)
        # Submit the services request and wait for its response
        def request(index, cancel=None):
            try: service.do('GET', f'http://origin-{index}.invalid/', cancel=cancel)
            except Exception as error: errors.append(error)
        with patch('stream_dock_manager_sdk.service.http.client.HTTPConnection', Connection):
            workers = [threading.Thread(target=request, args=(index,)) for index in range(64)]
            extra = threading.Thread(target=request, args=(64,))
            try:
                for worker in workers: worker.start()
                active = [entered.get(timeout=3) for _ in workers]
                extra.start()
                with self.assertRaises(queue.Empty): entered.get(timeout=.1)
                self.assertEqual(len(created), 64, 'a busy lease was evicted to admit another origin')
                active[0].release.set()
                admitted = entered.get(timeout=2)
                self.assertEqual(admitted.host, 'origin-64.invalid')
                self.assertTrue(active[0].closed)
                self.assertEqual(Connection.peak, 64)
            finally:
                for connection in created: connection.release.set()
                for worker in workers: worker.join(3)
                extra.join(3)
                service.close()
            self.assertEqual(errors, [])
            self.assertEqual(Connection.alive, 0)

    # Verify service cancellation interrupts detached response body
    def test_service_cancellation_interrupts_detached_response_body(self):
        from stream_dock_manager_sdk.service import Service
        from stream_dock_manager_sdk.testkit import Peer
        ready, release = threading.Event(), threading.Event()
        # Process the event in the services callback
        def handler(connection):
            data=b''
            while not data.endswith(b'\r\n\r\n'): data+=connection.recv(1)
            connection.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 99\r\nConnection: close\r\n\r\n{')
            ready.set()
            release.wait(3)
        with Peer(handler) as peer:
            service=Service(timeout=5);cancel=threading.Event();errors=[]
            # Submit the services request and wait for its response
            def request():
                try:service.do('GET',f'http://127.0.0.1:{peer.address[1]}/',cancel=cancel)
                except Exception as error:errors.append(error)
            worker=threading.Thread(target=request)
            try:
                worker.start();self.assertTrue(ready.wait(1));cancel.set();worker.join(1)
                self.assertFalse(worker.is_alive(),'detached response body did not respond to cancellation')
                self.assertEqual(len(errors),1)
            finally:
                release.set();service.close();worker.join(3)

    # Verify service response polling preserves framing and connection reuse
    def test_service_response_polling_preserves_framing_and_connection_reuse(self):
        from stream_dock_manager_sdk.service import Service
        body = b'{"value":1}'
        responses = [
            [b'HTTP/1.1 200', b' OK\r\nContent-Length: 11\r\nX-Test: ',
             b'fragmented\r\n\r\n' + body[:3], body[3:]],
            [b'HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nB',
             b'\r\n' + body[:3], body[3:] + b'\r\n0\r\nX-Test: trailer\r\n\r\n'],
        ]
        for secure in (False, True):
            with self.subTest(https=secure):
                connections, errors = [], []
                # Process the event in the services callback
                def handler(connection):
                    connections.append(connection)
                    try:
                        for parts in responses:
                            request = b''
                            while not request.endswith(b'\r\n\r\n'):
                                chunk = connection.recv(1)
                                if not chunk: return
                                request += chunk
                            for index, part in enumerate(parts):
                                if index: time.sleep(.12)
                                connection.sendall(part)
                    except Exception as error:
                        errors.append(error)
                with service_peer(handler, secure) as endpoint:
                    service = Service(timeout=3)
                    try:
                        for _ in responses:
                            self.assertEqual(service.json('GET', endpoint), {'value': 1})
                        self.assertEqual(len(connections), 1, 'successful responses did not reuse the connection')
                        self.assertEqual(errors, [])
                    finally:
                        service.close()

    # Verify service response polling keeps deadline and interrupts detached reads
    def test_service_response_polling_keeps_deadline_and_interrupts_detached_reads(self):
        import http.client
        from unittest.mock import patch
        from stream_dock_manager_sdk.service import Service
        original_read = http.client.HTTPResponse.read
        for secure in (False, True):
            for stop in ('deadline', 'cancel', 'close'):
                with self.subTest(https=secure, stop=stop):
                    body_started, release = threading.Event(), threading.Event()
                    # Read the next services value or data segment
                    def read(response, *args, **kwargs):
                        body_started.set()
                        return original_read(response, *args, **kwargs)
                    # Process the event in the services callback
                    def handler(connection):
                        request = b''
                        while not request.endswith(b'\r\n\r\n'):
                            chunk = connection.recv(1)
                            if not chunk: return
                            request += chunk
                        connection.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 99\r\nConnection: close\r\n\r\n{')
                        release.wait(3)
                    with service_peer(handler, secure) as endpoint, patch('http.client.HTTPResponse.read', read):
                        service = Service(timeout=1 if stop == 'deadline' else 3)
                        cancel, errors = threading.Event(), []
                        # Submit the services request and wait for its response
                        def request():
                            try: service.do('GET', endpoint, cancel=cancel)
                            except Exception as error: errors.append(error)
                        worker = threading.Thread(target=request)
                        try:
                            worker.start()
                            self.assertTrue(body_started.wait(1), f'body read did not start; request errors: {errors!r}')
                            with service.lock:
                                entries = list(service.connections.values())
                                self.assertEqual(len(entries), 1)
                                self.assertIsNone(entries[0].connection.sock, 'HTTP response did not detach its socket')
                            if stop == 'cancel': cancel.set()
                            elif stop == 'close': service.close()
                            worker.join(2 if stop == 'deadline' else 1)
                            self.assertFalse(worker.is_alive(), 'response wait renewed its deadline or ignored cancellation')
                            self.assertEqual(len(errors), 1)
                            if stop == 'deadline': self.assertIsInstance(errors[0], TimeoutError)
                        finally:
                            release.set()
                            service.close()
                            worker.join(3)
