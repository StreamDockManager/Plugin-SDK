"""Host-owned private state and portable VSD action/global settings.

No database driver, disk fallback, implicit retry, or private-to-global mapping.
"""
import json
import os
import re
import threading
import time
from .protocol import UnsupportedFeatureError

STORAGE_PROTOCOL = 'plugin-storage-v1'
SETTINGS_PROTOCOL = 'plugin-settings-rpc-v1'
RPC_REQUEST = 'streamDockManagerRpcRequest'
RPC_RESPONSE = 'streamDockManagerRpcResponse'
MAX_STORAGE_MESSAGE = 512 << 10
MAX_STORAGE_VALUE = 256 << 10


class RPCError(RuntimeError):
    # Retain the host error code alongside its message
    def __init__(self, code, message):
        self.code = code
        super().__init__(message)


# Validate the identifier used to correlate a storage operation
def operation_id():
    value = bytearray(os.urandom(16))
    value[6], value[8] = (value[6] & 15) | 64, (value[8] & 63) | 128
    text = value.hex()
    return '-'.join((text[:8], text[8:12], text[12:16], text[16:20], text[20:]))


# Return json for hoststorage
def _json(value):
    return json.dumps(value, ensure_ascii=False, separators=(',', ':'), allow_nan=False)


# Clone hoststorage data
def _clone(value):
    return json.loads(_json(value))


class Control:
    """Observe on the transport reader; never invoke application callbacks."""
    # Initialize correlated host requests and shared-read tracking
    def __init__(self, client):
        self.client = client
        self.lock = threading.Lock()
        self.requests, self.reads = {}, {}
        self.closed = False

    # Submit the control request and wait for its response
    def request(self, protocol, method, payload, cancel=None):
        if not self.client.supports(protocol): raise UnsupportedFeatureError(protocol)
        request = operation_id()
        message = dict(event=RPC_REQUEST, protocol_id=protocol, request_id=request,
                       method=method, payload=payload)
        if len(_json(message).encode()) > MAX_STORAGE_MESSAGE:
            raise RPCError('quota', 'storage request exceeds 512 KiB')
        return self._wait(self.requests, request, message, protocol, cancel)

    # Read the next control value or data segment
    def read(self, scope, context, cancel=None):
        key = 'global' if scope == 'global' else 'action:' + context
        message = dict(event='getGlobalSettings' if scope == 'global' else 'getSettings',
                       context=self.client.identity if scope == 'global' else context)
        return self._wait(self.reads, key, message, None, cancel)

    # Wait for the pending control operation
    def _wait(self, group, key, message, protocol, cancel):
        pending = dict(ready=threading.Event(), protocol=protocol)
        with self.lock:
            if self.closed or cancel is not None and cancel.is_set():
                raise RPCError('cancelled_before_commit', 'request cancelled before enqueue')
            if key in group or len(self.requests) + len(self.reads) >= 8:
                raise RPCError('busy', 'request already pending or eight-request capacity exhausted')
            group[key] = pending
        try:
            self.client._send_control(message)
            deadline = time.monotonic() + 5
            while not pending['ready'].wait(.05):
                if time.monotonic() >= deadline or cancel is not None and cancel.is_set():
                    raise RPCError('indeterminate', 'request timed out/cancelled after enqueue; reconcile before retrying')
            if 'error' in pending: raise pending['error']
            return pending['value']
        finally:
            with self.lock: group.pop(key, None)

    # Finalize the pending control operation
    @staticmethod
    def _finish(pending, value=None, error=None):
        if pending['ready'].is_set(): return
        if error is not None: pending['error'] = error
        else: pending['value'] = _clone(value)
        pending['ready'].set()

    # Record the current value and observation metadata
    def observe(self, message, raw_size=0):
        with self.lock:
            if message.get('event') == RPC_RESPONSE:
                pending = self.requests.get(message.get('request_id'))
                if pending is None: return True
                if (message.get('protocol_id') != pending['protocol'] or
                        raw_size > MAX_STORAGE_MESSAGE or len(_json(message).encode()) > MAX_STORAGE_MESSAGE or
                        ('result' in message) == ('error' in message)):
                    self._finish(pending, error=RPCError('invalid_response', 'invalid Manager RPC response'))
                elif 'error' in message:
                    failure = message['error']
                    if not isinstance(failure, dict) or not isinstance(failure.get('code'), str) or not isinstance(failure.get('message'), str):
                        self._finish(pending, error=RPCError('invalid_response', 'invalid Manager RPC error'))
                    else: self._finish(pending, error=RPCError(failure['code'], failure['message']))
                else: self._finish(pending, message['result'])
                return True
            name = message.get('event')
            key = 'global' if name == 'didReceiveGlobalSettings' else 'action:' + message.get('context', '') if name == 'didReceiveSettings' else None
            payload = message.get('payload')
            settings = payload.get('settings') if isinstance(payload, dict) else None
            if key in self.reads:
                if isinstance(settings, dict): self._finish(self.reads[key], settings)
                else: self._finish(self.reads[key], error=RPCError('invalid_response', 'settings response must be an object'))
            return False

    # Stop control activity and release owned resources
    def close(self):
        with self.lock:
            self.closed = True
            for group in (self.requests, self.reads):
                for pending in group.values():
                    self._finish(pending, error=RPCError('indeterminate', 'host disconnected; reconcile before retrying'))


class PortableSettings:
    """Reads observe state. Base replacements report sent, never committed."""
    # Bind settings access to the selected client, scope, and context
    def __init__(self, client, scope, context=''):
        if scope not in ('global', 'action') or scope == 'action' and not context:
            raise ValueError('an owned action context or global scope is required')
        self.client, self.scope, self.context = client, scope, context
        self.lock = threading.Lock()

    # Check whether the negotiated host supports enhanced settings operations
    def _enhanced(self, method, settings=None, cancel=None):
        payload = dict(scope=self.scope, context=self.context)
        if settings is not None: payload['settings'] = settings
        result = self.client.rpc(SETTINGS_PROTOCOL, method, payload, cancel=cancel)
        if (not isinstance(result, dict) or
                method == 'get' and (result.get('assurance') != 'observed' or not isinstance(result.get('settings'), dict)) or
                method == 'replace' and result.get('assurance') not in ('committed', 'applied_to_draft')):
            raise RPCError('invalid_response', 'invalid enhanced settings result')
        return dict(result, backend=SETTINGS_PROTOCOL)

    # Read the next portable settings value or data segment
    def _read(self, cancel=None):
        if self.client.supports(SETTINGS_PROTOCOL):
            try: return self._enhanced('get', cancel=cancel)
            except RPCError as error:
                if error.code != 'unsupported': raise
        return dict(settings=self.client.control.read(self.scope, self.context, cancel),
                    backend='base-vsd', assurance='observed')

    # Read the next portable settings value or data segment
    def read(self, cancel=None):
        with self.lock: return self._read(cancel)

    # Replace the selected value while preserving the surrounding policy
    def _replace(self, settings, cancel=None):
        if not isinstance(settings, dict): raise ValueError('settings must be an object')
        if cancel is not None and cancel.is_set(): raise RPCError('cancelled_before_commit', 'request cancelled before enqueue')
        if self.client.supports(SETTINGS_PROTOCOL):
            try: return self._enhanced('replace', settings, cancel)
            except RPCError as error:
                if error.code != 'unsupported': raise
        if self.scope == 'global': self.client.global_settings(settings)
        else: self.client.settings(self.context, settings)
        return dict(backend='base-vsd', assurance='sent')

    # Replace the selected value while preserving the surrounding policy
    def replace(self, settings, cancel=None):
        with self.lock: return self._replace(settings, cancel)

    # Update portable settings data
    def update(self, changes, cancel=None):
        with self.lock:
            current = self._read(cancel)['settings']
            current.update(changes)
            return self._replace(current, cancel)


# Validate a storage namespace or key before sending it to the host
def _name(value, limit):
    if not isinstance(value, str) or not value or len(value.encode()) > limit:
        raise ValueError(f'storage name must be 1–{limit} UTF-8 bytes')


# Return decimal for hoststorage
def _decimal(value):
    return isinstance(value, str) and re.fullmatch(r'0|[1-9][0-9]*', value) is not None


# Return uuid for hoststorage
def _uuid(value):
    return isinstance(value, str) and re.fullmatch(r'[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}', value) is not None


# Return generation for hoststorage
def _generation(value):
    return _decimal(value) and len(value) <= 39


# Return revision for hoststorage
def _revision(value):
    return _decimal(value) and (len(value) < 20 or len(value) == 20 and value <= '18446744073709551615')


class HostStore:
    """Explicit versions; no hidden cache, adoption, replay or weaker fallback."""
    # Bind private storage access to a validated namespace
    def __init__(self, client, namespace):
        if not client.supports(STORAGE_PROTOCOL): raise UnsupportedFeatureError(STORAGE_PROTOCOL)
        _name(namespace, 128)
        self.client, self.namespace = client, namespace

    # Correlate a host storage request with its response
    def _call(self, method, payload, cancel=None):
        result = self.client.rpc(STORAGE_PROTOCOL, method, dict(payload, namespace=self.namespace), cancel=cancel)
        valid = isinstance(result, dict) and _generation(result.get('generation'))
        if valid and (method != 'receipt' or result.get('found') is True or 'revision' in result): valid = _revision(result.get('revision'))
        if valid and method in ('get', 'receipt'): valid = type(result.get('found')) is bool
        if valid and method == 'get': valid = ('value' in result) == result['found']
        if valid and method == 'listKeys':
            valid = (isinstance(result.get('keys'), list) and len(result['keys']) <= 128
                     and all(isinstance(key, str) and key and len(key.encode()) <= 256 for key in result['keys'])
                     and ('cursor' not in result or isinstance(result['cursor'], str) and len(result['cursor'].encode()) <= 512))
        if valid and method in ('commit', 'receipt'):
            valid = result.get('operation_id') == payload['operation_id'] and (method == 'receipt' and result['found'] is False or result.get('committed') is True)
        if not valid: raise RPCError('invalid_response', 'invalid storage result')
        return result

    # Read a key through the negotiated private host storage capability
    def get(self, key, cancel=None):
        _name(key, 256)
        return self._call('get', dict(key=key), cancel)

    # List the keys visible in the selected host storage namespace
    def list_keys(self, cursor=None, cancel=None):
        if cursor is not None and (not isinstance(cursor, str) or len(cursor.encode()) > 512): raise ValueError('cursor exceeds 512 bytes')
        return self._call('listKeys', dict(cursor=cursor) if cursor else {}, cancel)

    # Commit the proposed state with revision conflict checks
    def commit(self, version, operation, mutations, cancel=None):
        if not _generation(version.get('generation')) or not _revision(version.get('revision')) or not _uuid(operation):
            raise ValueError('canonical generation, revision and operation ID required')
        if not isinstance(mutations, list) or not 1 <= len(mutations) <= 32:
            raise ValueError('commit requires 1–32 mutations')
        for mutation in mutations:
            _name(mutation.get('key'), 256)
            if mutation.get('op') == 'put':
                if 'value' not in mutation: raise ValueError('put requires a JSON value')
                if len(_json(mutation['value']).encode()) > MAX_STORAGE_VALUE: raise RPCError('quota', 'value exceeds 256 KiB')
            elif mutation.get('op') != 'delete': raise ValueError('mutation must be put or delete')
        return self._call('commit', dict(generation=version['generation'], expected_revision=version['revision'],
                         operation_id=operation, mutations=_clone(mutations)), cancel)

    # Validate the host storage operation receipt
    def receipt(self, generation, operation, cancel=None):
        if not _generation(generation) or not _uuid(operation): raise ValueError('canonical generation and operation ID required')
        return self._call('receipt', dict(generation=generation, operation_id=operation), cancel)
