"""Session-owned opt-in diagnostics; no global transport interception."""
import json
import re
import threading
import time

BODY_LIMIT = 64 << 10
HEADER_LIMIT = 8 << 10


# Check whether a header must be redacted from diagnostics
def sensitive_header(name):
    name = re.sub('[^a-z0-9]', '', name.lower())
    if name in ('authority', 'accesscontrolallowcredentials'):
        return False
    return bool(re.search('auth|cookie|token|secret|password|credential|signature|apikey|csrf|xsrf|hmac|bearer|sessionid', name) or name.endswith(('key', 'session')))


# Return sensitive for diagnostics
def sensitive(name):
    return bool(re.search('password|secret|token|api[_-]?key|authorization|cookie|webhook|credential|signature', name, re.I))


# Redact credentials and query values from the diagnostic endpoint URL
def endpoint(raw):
    from urllib.parse import urlsplit, urlunsplit, parse_qsl, urlencode
    try:
        url = urlsplit(raw)
        return urlunsplit((url.scheme, url.netloc.rsplit('@', 1)[-1], url.path,
                          urlencode([(key, '[redacted]') for key, _ in parse_qsl(url.query, keep_blank_values=True)]), ''))
    except ValueError:
        return '[endpoint]'


class Capture:
    # Initialize bounded body capture and completion flags
    def __init__(self, content_type=''):
        self.content_type = content_type if len(content_type.encode()) <= 256 else '[content type omitted]'
        self.data = bytearray()
        self.bytes = 0
        self.complete = self.truncated = False

    # Write the supplied capture value
    def write(self, data):
        self.bytes += len(data)
        if self.bytes > BODY_LIMIT:
            self.truncated = True
            self.data.clear()
        elif not self.truncated:
            self.data.extend(data)

    # Copy the current state for an isolated reader
    def snapshot(self, redact):
        import base64
        from urllib.parse import parse_qsl, urlencode
        try:
            text = self.data.decode('utf-8')
            binary = any(ord(c) < 32 and c not in '\r\n\t' or ord(c) == 127 for c in text)
        except UnicodeError:
            text, binary = self.data.decode('utf-8', errors='replace'), True
        original = text
        try:
            # Remove sensitive values from the captured diagnostic payload
            def clean(value, depth=0):
                if depth > 64: return '[redacted: nesting limit]'
                if isinstance(value, dict):
                    return {key: '[redacted]' if sensitive(key) else clean(item, depth+1) for key, item in value.items()}
                if isinstance(value, list):
                    return [clean(item, depth+1) for item in value]
                return redact(value) if isinstance(value, str) else value
            text = json.dumps(clean(json.loads(text)), ensure_ascii=False, allow_nan=False)
        except (ValueError, RecursionError):
            if text and (text.lstrip().startswith(('[', '{')) or 'json' in self.content_type.lower()):
                text = ''
                self.truncated = True
            if 'application/x-www-form-urlencoded' in self.content_type.lower():
                text = urlencode([(key, '[redacted]' if sensitive(key) else value) for key, value in parse_qsl(text, keep_blank_values=True)])
        text = redact(text)
        raw = bytes(self.data) if binary and text == original else text.encode()
        truncated = self.truncated or len(raw) > BODY_LIMIT
        if truncated:
            raw = b''
        return dict(text=base64.b64encode(raw).decode() if binary else raw.decode(),
                    encoding='base64' if binary else 'utf8', content_type=redact(self.content_type)[:256],
                    bytes=self.bytes, complete=self.complete, truncated=truncated)


class NetworkDiagnostics:
    # Initialize the active capture sink and request sequence counter
    def __init__(self):
        self._lock = threading.Lock()
        self._active = None
        self._sequence = 0

    # Select the active diagnostics sink and redaction policy
    def configure(self, enabled, sink, redact):
        with self._lock:
            if not enabled:
                self._active = None
            elif self._active is None:
                self._active = (sink, redact)

    # Report connection byte counts to the current diagnostics sink
    def io(self, operation, size):
        with self._lock:
            owner = self._active
        if owner:
            try:
                owner[0](dict(message=f'Connection {operation} · {size} bytes'), False)
            except Exception:
                pass

    # Start network diagnostics data
    def start(self, method, url, headers=None):
        with self._lock:
            owner = self._active
            if owner is None:
                return None
            self._sequence += 1
            sequence = self._sequence
        # Emit network diagnostics data
        def emit(payload, failed):
            with self._lock:
                active = self._active is owner
            if active:
                owner[0](payload, failed)
        return Trace(sequence, method, url, headers or {}, emit, owner[1])


# Collect headers within the diagnostic byte and field limits
def bounded_headers(headers):
    fields, used, overflow = {}, 0, False
    for name, raw in headers.items():
        values = list(raw) if isinstance(raw, (list, tuple)) else [str(raw)]
        cost = len(name.encode()) + sum(len(value.encode()) for value in values)
        if len(fields) >= 128 or len(name.encode()) > 256 or used + cost > BODY_LIMIT:
            overflow = True
            continue
        fields[name] = values
        used += cost
    return fields, overflow


class Trace:
    # Capture request metadata and initialize redacted body accounting
    def __init__(self, identity, method, url, headers, sink, redact):
        self.id, self.method, self.url = identity, method, url
        self.request_headers, self.headers_overflow = bounded_headers(headers)
        self.response_headers = {}
        self.request = Capture(next((str(v) for k, v in headers.items() if k.lower() == 'content-type'), ''))
        self.response = Capture()
        self.sink, self.redact = sink, redact
        self.started = time.monotonic()
        self.finished = False

    # Account for incoming response data in diagnostics
    def received(self, headers):
        self.response_headers, overflow = bounded_headers(headers)
        self.headers_overflow |= overflow
        value = next((v for k, v in headers.items() if k.lower() == 'content-type'), '')
        self.response = Capture(str(value[0] if isinstance(value, (list, tuple)) and value else value))

    # Finalize the pending trace operation
    def finish(self, status=0, failed=False):
        if self.finished:
            return
        self.finished = True
        try:
            self._finish(status, failed)
        except Exception:
            pass  # Diagnostics never change request results

    # Finalize the pending trace operation
    def _finish(self, status, failed):
        import base64
        from urllib.parse import quote, quote_plus
        secrets = set()
        overflow = self.headers_overflow
        used = 0
        # Retain the result for subsequent cache or diagnostic reads
        def remember(value):
            nonlocal used, overflow
            if not value or value in secrets:
                return
            used += len(value.encode())
            if len(secrets) >= 256 or used > BODY_LIMIT:
                overflow = True
            else:
                secrets.add(value)
        for headers in (self.request_headers, self.response_headers):
            for name, raw in headers.items():
                if not sensitive_header(name):
                    continue
                for value in raw if isinstance(raw, (list, tuple)) else [str(raw)]:
                    remember(value)
                    if 'auth' in name.lower():
                        remember(value.split(' ', 1)[-1])
                        if value.lower().startswith('basic '):
                            try:
                                decoded = base64.b64decode(value[6:]).decode()
                                remember(decoded)
                                for part in decoded.split(':'):
                                    remember(part)
                            except (ValueError, UnicodeError):
                                pass
                        for part in re.findall(r'(?:credential|username|response|nonce|cnonce)=["\']?([^,"\'\s]+)', value, re.I):
                            remember(part)
                    if 'cookie' in name.lower():
                        for pair in value.split(';'):
                            if '=' in pair:
                                remember(pair.split('=', 1)[1].strip())
        patterns = sorted({variant for value in secrets for variant in (value, value.encode().decode('latin1'), quote(value, safe=''), quote_plus(value), json.dumps(value, ensure_ascii=False)[1:-1])}, key=len, reverse=True)
        # Redact trace data
        def redact(text):
            if overflow:
                return '[redacted: credential capture limit exceeded]'
            for value in patterns:
                text = text.replace(value, '[redacted]')
            return self.redact(text)
        # Render bounded redacted headers for the captured request or response
        def headers(raw):
            entries, used, truncated = [], 0, overflow
            for name, values in sorted(raw.items()):
                if len(entries) >= 128 or len(name.encode()) > 256:
                    truncated = True
                    continue
                used += len(name.encode()) + 32
                safe = []
                for value in values if isinstance(values, (list, tuple)) else [str(values)]:
                    if sensitive_header(name):
                        value = '[redacted]'
                    elif name.lower() in ('location', 'content-location', 'referer', 'destination') or name.lower().endswith(('url', 'uri')) or value.startswith(('/', '?')):
                        value = endpoint(value)
                    else:
                        value = re.sub(r'[a-z][a-z0-9+.-]*://[^\s"<>]+', lambda m: endpoint(m[0]), value, flags=re.I)
                    value = redact(value)
                    cost = len(value.encode()) + 8
                    if used + cost > HEADER_LIMIT:
                        truncated = True
                        continue
                    used += cost
                    safe.append(value)
                if used > HEADER_LIMIT:
                    truncated = True
                    continue
                entries.append(dict(name=redact(name), values=safe))
            return dict(entries=entries, truncated=truncated)
        result = 'request failed or cancelled' if failed else f'HTTP {status}'
        method = re.sub('[\x00-\x1f\x7f]', '', self.method)
        self.sink(dict(message=redact(f'Request {self.id} · {method} {endpoint(self.url)} · {result} · {int((time.monotonic()-self.started)*1000)} ms'),
                       network=dict(request_id=str(self.id), request=self.request.snapshot(redact), response=self.response.snapshot(redact),
                                    request_headers=headers(self.request_headers), response_headers=headers(self.response_headers))), failed or status >= 400)


# Observe urllib's existing redirect and read paths without reading ahead
def http_handler(network):
    """Observe urllib's existing redirect and read paths without reading ahead."""
    from urllib.request import BaseHandler
    class Response:
        # Wrap the response with trace accounting and expected body length
        def __init__(self, response, trace):
            self.response, self.trace = response, trace
            grouped = {}
            for name, value in response.headers.items(): grouped.setdefault(name, []).append(value)
            trace.received(grouped)
            self.expected = response.headers.get('Content-Length')
        # Resolve delegated response attributes
        def __getattr__(self, name): return getattr(self.response, name)
        # Read the next response value or data segment
        def read(self, *args):
            try:
                data = self.response.read(*args)
            except Exception:
                self.trace.finish(self.response.code, True)
                raise
            self.trace.response.write(data)
            self.trace.response.complete = not data or str(self.trace.response.bytes) == self.expected
            return data
        # Stop response activity and release owned resources
        def close(self):
            try: return self.response.close()
            finally: self.trace.finish(self.response.code)
        # Enter the managed response scope
        def __enter__(self): return self
        # Release response resources when leaving the scope
        def __exit__(self, *args): self.close()
    class Diagnostics(BaseHandler):
        handler_order = 900
        # Create the collection of completed request traces
        def __init__(self): self.traces = []
        # Record the outbound HTTP request in diagnostics
        def http_request(self, request):
            trace = network.diagnostics.start(request.get_method(), request.full_url, dict(request.header_items())) if network else None
            request._sdk_trace = trace
            if trace:
                self.traces.append(trace)
                if request.data is None: trace.request.complete = True
                elif hasattr(request.data, 'trace'): request.data.trace = trace
            return request
        https_request = http_request
        # Record the HTTP response in diagnostics
        def http_response(self, request, response):
            trace = getattr(request, '_sdk_trace', None)
            return Response(response, trace) if trace else response
        https_response = http_response
        # Stop diagnostics activity and release owned resources
        def close(self):
            for trace in self.traces: trace.finish(0, True)
    return Diagnostics()
