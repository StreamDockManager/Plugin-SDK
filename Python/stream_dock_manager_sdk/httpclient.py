"""Bounded HTTP/JSON helpers, credential-aware validator cache and redacted previews."""
from collections import OrderedDict
from copy import deepcopy
from dataclasses import dataclass, field
from email.utils import parsedate_to_datetime
import base64
import hmac
import io
import json
import math
import os
import re
import threading
import time
from urllib.error import HTTPError
from urllib.parse import urlsplit, urlunsplit, parse_qs
from urllib.request import Request, build_opener, HTTPRedirectHandler
from .polling import retry_after

MAXIMUM_RESPONSE = 1 << 20


# Return no store for httpclient
def no_store(value): return any(part.strip().split('=', 1)[0].lower() == 'no-store' for part in value.split(','))
# Return valid etag for httpclient
def valid_etag(value): return len(value) <= 4096 and re.fullmatch(r'(?:W/)?"[\x21\x23-\x7e\x80-\xff]*"', value) is not None
# Validate a Last-Modified cache validator
def valid_modified(value):
    try: parsedate_to_datetime(value); return True
    except (TypeError, ValueError, OverflowError): return False


# Parse path
def parse_path(path):
    if len(path.encode()) > 8192: raise ValueError('path exceeds 8192 bytes')
    parts, index = [], 0
    while index < len(path):
        bracket = path[index] == '['
        if bracket: index += 1
        if index >= len(path): raise ValueError('unclosed bracket')
        literal = path[index] in "\"'"
        if literal and path[index] == '"':
            text, consumed = json.JSONDecoder().raw_decode(path[index:])
            index += consumed
        elif literal:
            quote, index, text = path[index], index + 1, ''
            while index < len(path) and path[index] != quote:
                char, index = path[index], index + 1
                if char == '\\':
                    if index >= len(path): raise ValueError('unfinished escape')
                    char, index = path[index], index + 1
                    if char == 'u':
                        if index + 4 > len(path): raise ValueError('short unicode escape')
                        char, index = chr(int(path[index:index + 4], 16)), index + 4
                    else: char = {'n': '\n', 'r': '\r', 't': '\t'}.get(char, char)
                text += char
            if index >= len(path): raise ValueError('unterminated quoted key')
            index += 1
        else:
            text = ''
            while index < len(path) and path[index] not in '.[]':
                char, index = path[index], index + 1
                if char == '\\':
                    if index >= len(path): raise ValueError('unfinished escape')
                    char, index = path[index], index + 1
                text += char
            if not text: raise ValueError('empty path component')
        numeric = not literal and text.isascii() and text.isdecimal()
        if bracket and not literal and not numeric: raise ValueError('brackets require index or quoted key')
        parts.append((text, int(text) if numeric else None))
        if len(parts) > 128: raise ValueError('path exceeds 128 components')
        if bracket:
            if index >= len(path) or path[index] != ']': raise ValueError('expected closing bracket')
            index += 1
        if index == len(path): break
        if path[index] == '[': continue
        if path[index] != '.': raise ValueError('expected dot or bracket')
        index += 1
        if index == len(path) or path[index] in '.[': raise ValueError('expected component after dot')
    return parts


# Extract path
def extract_path(root, path):
    value = root
    for key, index in parse_path(path):
        if isinstance(value, dict) and key in value: value = value[key]
        elif isinstance(value, list) and index is not None and index < len(value): value = value[index]
        else: return None, False
    return value, True


# Extract bounded candidate paths from the supplied value
def path_suggestions(root):
    result = []
    # Traverse nested values or files using the module’s bounds
    def walk(value, path, depth):
        if len(result) >= 256 or depth > 12: return
        result.append(path)
        if isinstance(value, dict):
            for key in sorted(value):
                simple = key and not any(c in key for c in '.[]\\"\'')
                child = (path + '.' if path else '') + key if simple else path + '[' + json.dumps(key, ensure_ascii=False) + ']'
                walk(value[key], child, depth + 1)
        elif isinstance(value, list):
            for index, item in enumerate(value[:8]): walk(item, f'{path}[{index}]', depth + 1)
    walk(root, '', 0)
    return result


# Check whether a field contains sensitive diagnostic data
def sensitive(key):
    key = key.lower()
    return key in ('key', 'signature') or any(part in key for part in ('token', 'password', 'secret', 'credential', 'authorization', 'cookie', 'api-key', 'api_key'))


# Expand environment-variable references in configured credentials
def expand(value):
    # Replace the selected value while preserving the surrounding policy
    def replace(match):
        name = match[1] or match[2]
        if name not in os.environ: raise ValueError(f'credential environment variable {name!r} is unavailable')
        return os.environ[name]
    return re.sub(r'\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)', replace, value)


# Redact httpclient data
def redact(value, secrets=(), depth=0):
    if depth > 10: return '[preview depth limit]'
    if isinstance(value, dict): return {key: '[redacted]' if sensitive(key) else redact(item, secrets, depth + 1) for key, item in list(value.items())[:128]}
    if isinstance(value, list): return [redact(item, secrets, depth + 1) for item in value[:32]]
    if isinstance(value, str):
        for secret in sorted(set(secrets), key=len, reverse=True):
            if secret: value = value.replace(secret, '[redacted]')
        return value[:4096] + ('…' if len(value) > 4096 else '')
    return value


@dataclass
class HTTPResult:
    status: int = 0
    duration_ms: int = 0
    fields: dict = field(default_factory=dict)
    missing: list = field(default_factory=list)
    preview: object = None
    paths: list = field(default_factory=list)
    revalidated: bool = False
    retry_at: float | None = None


class ValidatorCache:
    # Initialize validator entries, origin isolation salt, and clock
    def __init__(self, clock=time.monotonic):
        self.entries, self.salt, self.clock = OrderedDict(), os.urandom(32), clock
        self.epoch, self.lock = 0, threading.RLock()
    # Reset validator cache data
    def reset(self):
        with self.lock: self.epoch += 1; self.entries.clear()
    # Return key for validator cache
    def key(self, value): return hmac.digest(self.salt, json.dumps(value, sort_keys=True, separators=(',', ':'), default=str).encode(), 'sha256')
    # Start a new generation of tracked work
    def begin(self, identity):
        with self.lock:
            now = self.clock()
            for key in list(self.entries):
                if now - self.entries[key]['at'] > 1800: self.entries.pop(key)
            key = self.key(identity)
            prior = deepcopy(self.entries.get(key))
            if prior: self.entries.move_to_end(key)
            return key, self.epoch, prior
    # Store validator cache data
    def store(self, ticket, result, headers, target):
        key, epoch, _ = ticket
        with self.lock:
            if epoch != self.epoch: return
            self.entries.pop(key, None)
            if no_store(headers.get('Cache-Control', '')) or any(value.strip() == '*' for value in headers.get('Vary', '').split(',')): return
            etag, modified = headers.get('ETag', ''), headers.get('Last-Modified', '')
            if etag and (not valid_etag(etag)): etag = ''
            if modified:
                try: parsedate_to_datetime(modified)
                except (TypeError, ValueError): modified = ''
            data = deepcopy(result); data.preview, data.paths = None, []
            size = len(json.dumps(data.__dict__, default=str).encode()) + len(etag) + len(modified) + 64
            if not (etag or modified) or size > 256 << 10: return
            while self.entries and (len(self.entries) >= 64 or sum(item['size'] for item in self.entries.values()) + size > 8 << 20): self.entries.popitem(last=False)
            self.entries[key] = dict(result=data, etag=etag, modified=modified, target=self.key(target), at=self.clock(), size=size)


class Redirects(HTTPRedirectHandler):
    # Record the original destination and redirect policy
    def __init__(self, origin, no_redirect=False): self.origin, self.no_redirect, self.count = origin, no_redirect, 0
    # Apply the HTTP redirect policy before constructing a new request
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if self.no_redirect: return None
        self.count += 1
        parsed = urlsplit(newurl)
        if (parsed.scheme, parsed.netloc) != self.origin or self.count >= 5: raise ValueError('cross-origin or excessive redirect')
        return super().redirect_request(req, fp, code, msg, headers, newurl)


# Submit the httpclient request and wait for its response
def request(settings, *, cache=None, automatic=False, status_only=False, no_redirect=False, allow_user_info=False, cancel=None, network=None):
    start = time.monotonic()
    if cancel is not None and cancel.is_set(): raise RuntimeError('request cancelled or timed out')
    method = str(settings.get('method', 'GET')).upper()
    if method not in ('GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'): raise ValueError('unsupported HTTP method')
    if automatic and method not in ('GET', 'HEAD'): raise ValueError('automatic HTTP requests must be GET or HEAD')
    url, body = expand(settings.get('url', '')), expand(settings.get('body', ''))
    parsed = urlsplit(url)
    if parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username is not None and not allow_user_info: raise ValueError('request URL must be HTTP(S) without embedded credentials')
    timeout = settings.get('timeout', 10)
    if isinstance(timeout, str): timeout = float(timeout)
    if type(timeout) not in (int, float) or not math.isfinite(timeout) or not 0 < timeout <= 120: raise ValueError('invalid request timeout')
    fields = settings.get('fields', {})
    if not isinstance(fields, dict) or len(fields) > 32: raise ValueError('configure at most 32 named fields')
    for path in fields.values(): parse_path(path)
    headers = {key: expand(value) for key, value in settings.get('headers', {}).items()}
    secrets = []
    # Inspect nested values for bounded diagnostic extraction
    def scan(value, key=''):
        if isinstance(value, dict):
            for key, item in value.items(): scan(item, key)
        elif isinstance(value, list):
            for item in value: scan(item, key)
        elif isinstance(value, str):
            for match in re.finditer(r'\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)', value): secrets.append(os.environ.get(match[1] or match[2], ''))
            if sensitive(key):
                secrets.append(value)
                if key.lower() == 'authorization' and ' ' in value: secrets.append(value.split(' ', 1)[1])
                if key.lower() == 'cookie': secrets.extend(part.split('=', 1)[1].strip() for part in value.split(';') if '=' in part)
    scan(settings); scan(headers); scan(parse_qs(parsed.query))
    try: scan(json.loads(body))
    except ValueError: pass
    if parsed.password:
        secrets.append(parsed.password)
        credentials = base64.b64encode(f'{parsed.username}:{parsed.password}'.encode()).decode()
        headers['Authorization'] = 'Basic ' + credentials
        url = urlunsplit(parsed._replace(netloc=parsed.netloc.rsplit('@', 1)[1]))
        parsed = urlsplit(url)
    ticket = None
    conditional = any(key.lower() in ('if-match', 'if-none-match', 'if-modified-since', 'if-unmodified-since', 'if-range', 'range') for key in headers)
    if cache and automatic and method == 'GET' and not conditional:
        ticket = cache.begin([method, url, headers, body, fields, settings.get('field'), status_only, no_redirect, allow_user_info, sorted(secrets)])
        if no_store(next((v for k, v in headers.items() if k.lower() == 'cache-control'), '')):
            with cache.lock: cache.entries.pop(ticket[0], None)
            ticket = None
        if ticket and ticket[2]:
            prior = ticket[2]
            headers['If-None-Match' if prior['etag'] else 'If-Modified-Since'] = prior['etag'] or prior['modified']
    class Upload(io.BytesIO):
        trace = None
        # Read the next upload value or data segment
        def read(self, *args):
            chunk = super().read(*args)
            if network: network.add(sent=len(chunk))
            if self.trace:
                self.trace.request.write(chunk)
                self.trace.request.complete = self.tell() == len(self.getbuffer())
            return chunk
    upload = Upload(body.encode()) if body else None
    if body: headers['Content-Length'] = str(len(body.encode()))
    outgoing = Request(url, data=upload, headers=headers, method=method)
    from .diagnostics import http_handler
    diagnostics = http_handler(network)
    try:
        try:
            response = build_opener(Redirects((parsed.scheme, parsed.netloc), no_redirect), diagnostics).open(outgoing, timeout=timeout)
        except HTTPError as response_error: response = response_error
        except Exception as error: raise RuntimeError('request failed (check URL, network, TLS and redirect policy)') from error
        with response:
            if cancel is not None and cancel.is_set(): raise RuntimeError('request cancelled or timed out')
            result = HTTPResult(status=response.code, duration_ms=int((time.monotonic() - start) * 1000), retry_at=retry_after(response.headers.get('Retry-After', ''), time.time()))
            if automatic and method == 'GET' and response.code == 304:
                key, epoch, prior = ticket if ticket else (None, None, None)
                with cache.lock if cache else threading.RLock():
                    current = cache.entries.get(key) if cache else None
                    valid = prior and epoch == cache.epoch and current and current == prior and cache.key(response.geturl()) == prior['target']
                received = response.headers.get('ETag', '')
                modified = response.headers.get('Last-Modified', '')
                valid = valid and (not received or valid_etag(received)) and (not modified or valid_modified(modified))
                valid = valid and (prior['etag'] or not modified or modified == prior['modified'])
                valid = valid and (not received or not prior['etag'] or received.removeprefix('W/') == prior['etag'].removeprefix('W/'))
                if not valid:
                    if cache:
                        with cache.lock: cache.entries.pop(key, None)
                    raise ValueError('HTTP 304 has no matching retained response')
                old = deepcopy(prior['result']); old.status, old.duration_ms, old.revalidated, old.retry_at = result.status, result.duration_ms, True, result.retry_at
                merged = {'ETag': prior['etag'], 'Last-Modified': prior['modified'], **dict(response.headers)}
                cache.store(ticket, old, merged, response.geturl())
                return old
            if not status_only:
                data = bytearray()
                while True:
                    if cancel is not None and cancel.is_set() or time.monotonic() - start > timeout: raise RuntimeError('request cancelled or timed out')
                    chunk = response.read(min(65536, MAXIMUM_RESPONSE + 1 - len(data)))
                    if network: network.add(received=len(chunk))
                    data.extend(chunk)
                    if len(data) > MAXIMUM_RESPONSE: raise ValueError('response exceeds 1 MiB')
                    if not chunk: break
                if method != 'HEAD' and data:
                    try:
                        text = data.decode('utf-8').lstrip()
                        root, consumed = json.JSONDecoder().raw_decode(text)
                    except (ValueError, UnicodeError): result.preview = redact(data[:4096].decode(errors='replace'), secrets)
                    else:
                        if text[consumed:].strip(): raise ValueError('response contains trailing JSON data')
                        result.preview, result.paths = redact(root, secrets), path_suggestions(root)
                        for name, path in fields.items():
                            value, found = extract_path(root, path)
                            if found: result.fields[name] = redact(value, secrets)
                            else: result.missing.append(name)
            result.missing.sort(); result.duration_ms = int((time.monotonic() - start) * 1000)
            if ticket and (response.code == 200 or no_store(response.headers.get('Cache-Control', ''))): cache.store(ticket, result, response.headers, response.geturl())
            return result
    finally:
        diagnostics.close()

from .service import Service, HTTPError as ServiceHTTPError
