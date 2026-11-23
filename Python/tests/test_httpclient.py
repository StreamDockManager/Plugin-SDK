import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import sys
import threading
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from stream_dock_manager_sdk.httpclient import request, ValidatorCache
from stream_dock_manager_sdk.network import NetworkPayload


class Server(BaseHTTPRequestHandler):
    requests = []
    # Handle the fixture server log without changing its response
    def log_message(self, *args): pass
    # Serve the fixture HTTP POST request
    def do_POST(self): self.do_GET()
    # Serve the fixture HTTP GET request
    def do_GET(self):
        Server.requests.append((self.command, self.path, dict(self.headers)))
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        payload = json.dumps(dict(value=42, echo=self.headers.get('Authorization', '').removeprefix('Bearer '), body=body.decode())).encode()
        status, headers = 200, {}
        if self.path == '/cache':
            headers['ETag'] = '"v1"'
            if self.headers.get('If-None-Match') == '"v1"': status, payload = 304, b''
        if self.path == '/broken304': status, payload = 304, b''
        if self.path == '/redirect': status, headers['Location'], payload = 302, '/json', b''
        if self.path == '/outside': status, headers['Location'], payload = 302, 'http://127.0.0.1:1/forbidden', b''
        if self.path == '/trailing': payload = b'{} trailing'
        if self.path == '/badvalidator':
            headers['ETag'] = 'invalid' if self.headers.get('If-None-Match') else '"v1"'
            if self.headers.get('If-None-Match'): status, payload = 304, b''
        if self.path == '/large': payload = b'x' * ((1 << 20) + 1)
        self.send_response(status)
        for name, value in headers.items(): self.send_header(name, value)
        self.send_header('Content-Length', str(len(payload))); self.end_headers()
        try: self.wfile.write(payload)
        except (BrokenPipeError, ConnectionResetError): pass


class HTTPTests(unittest.TestCase):
    # Prepare isolated fixtures for httptests checks
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(('127.0.0.1', 0), Server)
        cls.worker = threading.Thread(target=cls.server.serve_forever, daemon=True); cls.worker.start()
        cls.url = f'http://127.0.0.1:{cls.server.server_address[1]}'
    # Release fixtures created by httptests checks
    @classmethod
    def tearDownClass(cls): cls.server.shutdown(); cls.server.server_close(); cls.worker.join()

    # Verify real transport extracts redacts and counts bodies
    def test_real_transport_extracts_redacts_and_counts_bodies(self):
        counters = NetworkPayload()
        with patch.dict('os.environ', {'SDK_TEST_SECRET': 'private-token'}):
            value = request(dict(url=self.url+'/json', headers=dict(Authorization='Bearer ${SDK_TEST_SECRET}'), fields=dict(value='value', echo='echo')),
                            network=counters)
        self.assertEqual(value.fields, dict(value=42, echo='[redacted]'))
        self.assertGreater(counters.snapshot()[1], 0)
        sent = request(dict(url=self.url+'/post', method='POST', body='payload'), network=counters)
        self.assertEqual(sent.preview['body'], 'payload')
        self.assertEqual(counters.snapshot()[0], len('payload'))

    # Verify validator cache scope reset and 304
    def test_validator_cache_scope_reset_and_304(self):
        cache = ValidatorCache()
        settings = dict(url=self.url+'/cache', fields=dict(value='value'))
        display = request(settings, automatic=True, cache=cache, preview=False)
        self.assertEqual(display.fields, dict(value=42))
        self.assertIsNone(display.preview)
        self.assertEqual(display.paths, [])
        first = request(settings, automatic=True, cache=cache)
        second = request(settings, automatic=True, cache=cache)
        self.assertFalse(first.revalidated); self.assertTrue(second.revalidated)
        self.assertEqual(second.fields, dict(value=42))
        # A changed credential has its own validator identity
        third = request(dict(settings, headers=dict(Authorization='Bearer new')), automatic=True, cache=cache)
        self.assertFalse(third.revalidated)
        cache.reset(); self.assertFalse(request(settings, automatic=True, cache=cache).revalidated)
        with self.assertRaises(ValueError): request(dict(url=self.url+'/broken304'), automatic=True, cache=cache)

    # Verify redirect bounds cancellation and automatic mutations
    def test_redirect_bounds_cancellation_and_automatic_mutations(self):
        self.assertEqual(request(dict(url=self.url+'/redirect')).status, 200)
        self.assertEqual(request(dict(url=self.url+'/redirect'), no_redirect=True).status, 302)
        with self.assertRaises(RuntimeError): request(dict(url=self.url+'/outside'))
        with self.assertRaises(ValueError): request(dict(url=self.url+'/large'))
        cancelled = threading.Event(); cancelled.set()
        before = len(Server.requests)
        with self.assertRaises(RuntimeError): request(dict(url=self.url+'/json'), cancel=cancelled)
        with self.assertRaises(ValueError): request(dict(url=self.url+'/post', method='POST'), automatic=True)
        self.assertEqual(before, len(Server.requests))

    # Verify trailing json and invalid revalidation
    def test_trailing_json_and_invalid_revalidation(self):
        with self.assertRaisesRegex(ValueError, 'trailing JSON'): request(dict(url=self.url+'/trailing'))
        cache = ValidatorCache()
        settings = dict(url=self.url+'/badvalidator')
        request(settings, automatic=True, cache=cache)
        with self.assertRaisesRegex(ValueError, 'matching retained'): request(settings, automatic=True, cache=cache)
        self.assertEqual(cache.entries, {})
