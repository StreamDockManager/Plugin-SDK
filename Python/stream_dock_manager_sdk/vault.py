"""Typed, capability-gated Vault text reads over the authenticated host RPC.

No local files, environment lookup, fallback, implicit retry, or grant creation.
The host remains responsible for binding, action ownership and access policy.
"""
from dataclasses import dataclass, field as dataclass_field
import re
import threading
from typing import Literal, TypedDict, cast

from .hoststorage import RPCError
from .protocol import UnsupportedFeatureError

VAULT_VALUES_PROTOCOL = 'plugin-vault-values-v1'
VAULT_SECRET_SHARING_PROTOCOL = 'plugin-vault-secret-sharing-v1'
MAX_VAULT_TEXT_BYTES = 64 << 10
_SECRET_KINDS = {'api-key', 'bearer-token', 'credential'}
_ERROR_CODES = {'needs_setup', 'vault_locked', 'permission_denied', 'not_declared',
                'operation_unavailable', 'unsupported', 'stale_revision', 'cancelled',
                'cancelled_before_commit', 'timeout', 'rate_limited', 'busy',
                'indeterminate', 'invalid_request', 'invalid_response', 'quota', 'slot_conflict', 'activation_required', 'activation_expired', 'no_text', 'unsupported_format', 'invalid_encoding', 'too_large', 'clipboard_changed', 'clipboard_access'}
_IDENTIFIER = re.compile(r'[A-Za-z0-9_.-]+', re.ASCII)
_REVISION = re.compile(r'(?:0|[1-9][0-9]{0,19})', re.ASCII)


@dataclass(frozen=True)
class VaultTextValue:
    """A host-returned text value. repr intentionally excludes its contents."""
    value: str = dataclass_field(repr=False)
    revision: str
    entry_kind: Literal['text'] = 'text'


@dataclass(frozen=True)
class VaultLocation:
    latitude: float = dataclass_field(repr=False)
    longitude: float = dataclass_field(repr=False)
    revision: str


class VaultSecretValue:
    """Non-serializable-by-default secret; call reveal() explicitly to use it."""
    __slots__ = ('__value', 'revision', 'entry_kind')

    def __init__(self, value: str, revision: str, entry_kind: str = 'api-key'):
        self.entry_kind = entry_kind
        self.__value = value
        self.revision = revision

    def reveal(self) -> str:
        return self.__value

    def __repr__(self) -> str:
        return 'VaultSecretValue([redacted])'

    def __reduce__(self):
        raise TypeError('Vault secrets cannot be serialized implicitly')


def _identifier(value: str, limit: int, name: str) -> str:
    if not isinstance(value, str) or not 1 <= len(value) <= limit or _IDENTIFIER.fullmatch(value) is None:
        raise ValueError(f'{name} must be 1–{limit} ASCII letters, digits, underscores, dots or hyphens')
    return value


def _decode_text(result: object, kind: str = 'text') -> VaultTextValue | VaultSecretValue:
    if not isinstance(result, dict) or set(result) != {'entry_kind', 'value', 'revision'}:
        raise RPCError('invalid_response', 'invalid Vault text response')
    value, revision = result['value'], result['revision']
    entry_kind = result['entry_kind']
    if kind == 'secret' and (not isinstance(entry_kind, str) or entry_kind not in _SECRET_KINDS):
        raise RPCError('invalid_response', 'invalid Vault secret response')
    if (kind == 'text' and entry_kind != 'text') or not isinstance(value, str):
        raise RPCError('invalid_response', 'invalid Vault text response')
    try:
        valid_size = len(value) <= MAX_VAULT_TEXT_BYTES and len(value.encode('utf-8')) <= MAX_VAULT_TEXT_BYTES
    except UnicodeEncodeError:
        valid_size = False
    if not valid_size or not isinstance(revision, str) or _REVISION.fullmatch(revision) is None or int(revision) > (1 << 64) - 1:
        raise RPCError('invalid_response', 'invalid Vault text response')
    if kind == 'secret':
        return VaultSecretValue(value=value, revision=revision, entry_kind=entry_kind)
    return VaultTextValue(value=value, revision=revision)


VAULT_WRITES_PROTOCOL = 'plugin-vault-writes-v1'
_UUID = r'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'
_ENTRY_REF = re.compile(_UUID + r'\.' + _UUID)

class VaultWriteReceipt(TypedDict):
    entry_ref: str
    revision: str

def _positive_revision(value: object) -> bool:
    return isinstance(value,str) and bool(_REVISION.fullmatch(value)) and 0 < int(value) < 2**64

def private_request(client, protocol, method, payload, cancel=None):
    try:
        return client.rpc(protocol, method, payload, cancel=cancel)
    except RPCError as error:
        code = error.code if isinstance(error.code, str) and error.code in _ERROR_CODES else 'operation_unavailable'
        raise RPCError(code, 'Manager request did not complete') from None


class Vault:
    """Read only explicitly bound text values; construction grants no access."""

    def __init__(self, client):
        if not (client.supports(VAULT_VALUES_PROTOCOL) or client.supports(VAULT_SECRET_SHARING_PROTOCOL) or client.supports(VAULT_WRITES_PROTOCOL)):
            raise UnsupportedFeatureError(VAULT_VALUES_PROTOCOL)
        self._client = client

    def read_value(self, slot: str, field: str, action: str,
                   cancel: threading.Event | None = None) -> VaultTextValue:
        """Read once using host authorization, preserving host RPC failures."""
        # Recheck negotiation on each call; never downgrade to another protocol.
        if not self._client.supports(VAULT_VALUES_PROTOCOL):
            raise UnsupportedFeatureError(VAULT_VALUES_PROTOCOL)
        payload = dict(slot=_identifier(slot, 128, 'slot'),
                       field=_identifier(field, 128, 'field'),
                       action=_identifier(action, 256, 'action'))
        return cast(VaultTextValue, _decode_text(private_request(self._client, VAULT_VALUES_PROTOCOL, 'read-value', payload, cancel)))

    def read_location(self, slot: str, field: str, action: str,
                      cancel: threading.Event | None = None) -> VaultLocation:
        if not self._client.supports(VAULT_VALUES_PROTOCOL):
            raise UnsupportedFeatureError(VAULT_VALUES_PROTOCOL)
        payload = dict(slot=_identifier(slot, 128, 'slot'), field=_identifier(field, 128, 'field'),
                       action=_identifier(action, 256, 'action'))
        result = private_request(self._client, VAULT_VALUES_PROTOCOL, 'read-location', payload, cancel)
        if isinstance(result, dict) and set(result) == {'value', 'revision', 'entry_kind'} and result['entry_kind'] == 'location':
            coords, revision = result['value'], result['revision']
            if isinstance(coords, dict) and set(coords) == {'latitude', 'longitude'} and (revision == '0' or _positive_revision(revision)):
                lat, lon = coords['latitude'], coords['longitude']
                if type(lat) in (float, int) and -90 <= lat <= 90 and type(lon) in (float, int) and -180 <= lon <= 180:
                    return VaultLocation(lat, lon, revision)
        raise RPCError('invalid_response', 'Invalid Vault location response')

    def read_secret(self, slot: str, field: str, action: str,
                    cancel: threading.Event | None = None) -> VaultSecretValue:
        """Explicitly request an authorized API key; never fall back to text."""
        if not self._client.supports(VAULT_SECRET_SHARING_PROTOCOL):
            raise UnsupportedFeatureError(VAULT_SECRET_SHARING_PROTOCOL)
        payload = dict(slot=_identifier(slot, 128, 'slot'),
                       field=_identifier(field, 128, 'field'),
                       action=_identifier(action, 256, 'action'))
        return cast(VaultSecretValue, _decode_text(private_request(self._client, VAULT_SECRET_SHARING_PROTOCOL, 'read-secret', payload, cancel), 'secret'))

    def write_secret(self, slot: str, field: str, action: str, kind: str, value: str,
                     expected_revision: str | None = None, cancel: threading.Event | None = None) -> VaultWriteReceipt:
        if not self._client.supports(VAULT_WRITES_PROTOCOL):
            raise UnsupportedFeatureError(VAULT_WRITES_PROTOCOL)
        if (kind not in _SECRET_KINDS or not isinstance(value, str) or not value or
            re.search(r'[\ud800-\udfff]',value) or len(value.encode('utf-8')) > MAX_VAULT_TEXT_BYTES or
            (expected_revision is not None and not _positive_revision(expected_revision))):
            raise RPCError('invalid_request','Invalid Vault write')
        payload = dict(slot=_identifier(slot,128,'slot'), field=_identifier(field,128,'field'),
                       action=_identifier(action,256,'action'), entry_kind=kind, value=value, operation='create')
        if expected_revision is not None:
            payload.update(operation='rotate',expected_revision=expected_revision)
        receipt = private_request(self._client, VAULT_WRITES_PROTOCOL,'write-secret',payload,cancel)
        if (not isinstance(receipt,dict) or set(receipt) != {'entry_ref','revision'} or
            not isinstance(receipt['entry_ref'],str) or not _ENTRY_REF.fullmatch(receipt['entry_ref']) or
            not _positive_revision(receipt['revision'])):
            raise RPCError('invalid_response','Invalid Vault write receipt')
        return cast(VaultWriteReceipt,receipt)

TEXT_REFERENCES_PROTOCOL = 'plugin-text-references-v1'


def format_text(client, action: str, text: str,
                cancel: threading.Event | None = None) -> str:
    """Resolve one transient string using the host's action-scoped permission."""
    if not _fits_utf8(text, 64 << 10):
        raise RPCError('invalid_request', 'Invalid text to format')
    if '}' not in text.partition('${')[2]:
        return text
    if not client.supports(TEXT_REFERENCES_PROTOCOL):
        raise UnsupportedFeatureError(TEXT_REFERENCES_PROTOCOL)
    try:
        action = _identifier(action, 256, 'action')
    except ValueError:
        raise RPCError('invalid_request', 'Invalid text action') from None
    result = private_request(client, TEXT_REFERENCES_PROTOCOL, 'format-text',
                             dict(action=action, text=text), cancel)
    if not isinstance(result, dict) or set(result) != {'text'} or not _fits_utf8(result['text'], 256 << 10):
        raise RPCError('invalid_response', 'Invalid formatted text response')
    return result['text']


# Reject unpaired surrogates before the JSON transport can replace them
def _fits_utf8(value, limit: int) -> bool:
    if not isinstance(value, str) or len(value) > limit:
        return False
    try:
        return len(value.encode('utf-8')) <= limit
    except UnicodeEncodeError:
        return False
