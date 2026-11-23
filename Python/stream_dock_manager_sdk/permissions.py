"""Action-scoped Manager services on the existing authenticated connection."""
from dataclasses import dataclass
from threading import Event
from .protocol import Event as HostEvent, UnsupportedFeatureError
from .hoststorage import RPCError
from typing import Literal, TypedDict
import re
from .runtime import Client
from .vault import private_request

PERMISSIONS_PROTOCOL = 'plugin-permissions-v1'
HTTP_BROKER_PROTOCOL = 'plugin-http-broker-v1'
NOTIFICATIONS_PROTOCOL = 'plugin-notifications-v1'
NotificationLevel = Literal['info', 'success', 'warning', 'error']


class PermissionStatus(TypedDict):
    revision: str
    granted: bool
    available: bool


class ActionPermissionStatus(TypedDict):
    action: str
    scope: dict[str, str]
    granted: bool
    available: bool


class PermissionList(TypedDict):
    revision: str
    scopes: list[ActionPermissionStatus]


class PermissionReview(TypedDict):
    review_id: str
    revision: str
    state: str


class RecipeResult(TypedDict):
    result_handle: str
    state: str


class ClipboardText(TypedDict):
    text: str
    revision: str

class CompletionStatus(TypedDict):
    state: str

VAULT_REFERENCES_PROTOCOL = 'plugin-vault-references-v1'

class VaultEntryName(TypedDict):
    id: str
    name: str

class VaultEntryPage(TypedDict):
    entries: list[VaultEntryName]
    next: int | None

class Permissions:
    def __init__(self, client: Client):
        self._client = client

    def get_permissions(self, cancel: Event | None = None) -> PermissionList:
        return private_request(self._client, PERMISSIONS_PROTOCOL, 'get-permissions', {}, cancel=cancel)

    def has_permission(self, action: str, scope: dict[str, str], cancel: Event | None = None) -> PermissionStatus:
        return private_request(self._client, PERMISSIONS_PROTOCOL, 'has-permission', {'action': action, 'scope': scope}, cancel=cancel)

    def request_permissions(self, activation_token: str, scopes: list[dict[str, str]], cancel: Event | None = None) -> PermissionReview:
        return private_request(self._client, PERMISSIONS_PROTOCOL, 'request-permissions', {'activation_token': activation_token, 'scopes': scopes}, cancel=cancel)

    def send_example_webhook(self, activation_token: str, certificate: str, cancel: Event | None = None) -> RecipeResult:
        return private_request(self._client, HTTP_BROKER_PROTOCOL, 'manager-webhook', {'activation_token': activation_token, 'certificate': certificate}, cancel=cancel)
    def send_local_webhook(self, activation_token: str, origin: str, cancel: Event | None = None) -> RecipeResult:
        return private_request(self._client, HTTP_BROKER_PROTOCOL, 'manager-local-webhook', {'activation_token': activation_token, 'origin': origin}, cancel=cancel)

    def summarize_clipboard(self, activation_token: str, cancel: Event | None = None) -> RecipeResult:
        return private_request(self._client, HTTP_BROKER_PROTOCOL, 'clipboard-summary', {'activation_token': activation_token}, cancel=cancel)

    def read_clipboard_text(self, activation_token: str, cancel: Event | None = None) -> ClipboardText:
        return private_request(self._client, PERMISSIONS_PROTOCOL,'read-clipboard-text',{'activation_token':activation_token},cancel=cancel)

    def show_text_result(self, activation_token: str, text: str, cancel: Event | None = None) -> RecipeResult:
        return private_request(self._client, PERMISSIONS_PROTOCOL,'show-text-result',{'activation_token':activation_token,'text':text},cancel=cancel)

    def notify_completion(self, activation_token: str, cancel: Event | None = None) -> CompletionStatus:
        return private_request(self._client, PERMISSIONS_PROTOCOL,'notify-completion',{'activation_token':activation_token},cancel=cancel)

    def create_notification(self, action: str, level: NotificationLevel, message: str, cancel: Event | None = None) -> None:
        """Send a plain-text notice under the reviewed action scope."""
        if not self._client.supports(NOTIFICATIONS_PROTOCOL):
            raise UnsupportedFeatureError(NOTIFICATIONS_PROTOCOL)
        if not isinstance(action, str) or re.fullmatch(r'[A-Za-z0-9_.-]{1,256}', action) is None or level not in ('info', 'success', 'warning', 'error') or not isinstance(message, str) or not message.strip():
            raise RPCError('invalid_request', 'Invalid notification action, level or message')
        try:
            size = len(message.encode('utf-8'))
        except UnicodeEncodeError:
            raise RPCError('invalid_request', 'Invalid notification message') from None
        if size > 512:
            raise RPCError('too_large', 'Notification message exceeds 512 UTF-8 bytes')
        receipt = private_request(self._client, PERMISSIONS_PROTOCOL, 'create-notification', {'action': action, 'level': level, 'message': message}, cancel=cancel)
        if not isinstance(receipt, dict) or receipt.get('state') != 'created':
            raise RPCError('invalid_response', 'Invalid notification receipt')

    def list_vault_entries(self, action: str, scope: dict[str,str], start: int = 0, cancel: Event | None = None) -> VaultEntryPage:
        return private_request(self._client, VAULT_REFERENCES_PROTOCOL,'list-vault-entries',{'action':action,'scope':scope,'start':start},cancel=cancel)

    def select_vault_entry(self, activation_token: str, action: str, scope: dict[str,str], cancel: Event | None = None) -> PermissionReview:
        return private_request(self._client, VAULT_REFERENCES_PROTOCOL,'select-vault-entry',{'action':action,'scope':scope,'activation_token':activation_token},cancel=cancel)

    def run_vault_recipe(self, activation_token: str, scope: dict[str,str], entry_ref: str, cancel: Event | None = None) -> RecipeResult:
        method=scope.get('recipe')
        if method != 'clipboard-summary':
            from .hoststorage import RPCError
            raise RPCError('invalid_request','Unknown Vault recipe')
        return private_request(self._client, HTTP_BROKER_PROTOCOL,method,{'scope':scope,'entry_ref':entry_ref,'activation_token':activation_token},cancel=cancel)

@dataclass(frozen=True)
class PermissionNotice:
    event: str
    request_id: str = ''
    review_id: str = ''
    outcome: str = ''
    revision: str = ''
    action: str = ''
    scopes: tuple[dict[str, str], ...] = ()
    entry: VaultEntryName | None = None
    result_handle: str = ''


def permission_notice(event: HostEvent) -> PermissionNotice | None:
    """Project an incoming Manager notice into its typed, operation-specific fields."""
    wire = event.raw
    try:
        if event.event == 'streamDockManagerPermissionRequestResult':
            return PermissionNotice(event.event, wire['request_id'], wire['review_id'],
                                    wire['outcome'], wire['revision'])
        if event.event == 'streamDockManagerPermissionsChanged':
            return PermissionNotice(event.event, revision=wire['revision'], action=wire['action'],
                                    scopes=tuple(wire['scopes']))
        if event.event == 'streamDockManagerVaultSelectionResult':
            chosen = wire.get('entry')
            entry = VaultEntryName(id=chosen['id'], name=chosen['name']) if chosen else None
            return PermissionNotice(event.event, wire['request_id'], wire['review_id'],
                                    wire['outcome'], entry=entry)
    except (KeyError, TypeError):
        return None
    return None
