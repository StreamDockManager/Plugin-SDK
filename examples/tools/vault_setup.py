"""Validate v1 Manager setup declarations without resolving entries or executing code."""
import ipaddress
import re
import unicodedata

_KEYS = {'id', 'kind', 'entry_kind', 'mode', 'required', 'purpose', 'field', 'action'}
_PAIRS = {'location': {'location'}, 'value': {'text'}, 'secret': {'api-key', 'bearer-token', 'credential'}}


def _identifier(value, maximum):
    return isinstance(value, str) and len(value) <= maximum and re.fullmatch(r'[A-Za-z0-9_.-]+', value)


def _purpose(value):
    return (isinstance(value, str) and bool(value.strip()) and len(value.encode()) <= 512
            and all(unicodedata.category(char) != 'Cc' for char in value))


def _loopback_origin(origin):
    match = re.fullmatch(r'http://(localhost|\[::1\]|[0-9.]+)(?::([1-9][0-9]{0,4}))?', origin)
    if not match or int(match[2] or 80) > 65535:
        return False
    if match[1] in ('localhost', '[::1]'):
        return True
    try:
        return ipaddress.IPv4Address(match[1]).is_loopback
    except ValueError:
        return False


def _canonical_scope(scope):
    if not isinstance(scope, dict) or not all(isinstance(v, str) for v in scope.values()):
        raise ValueError('Invalid Manager permission scope')
    scope = dict(scope)
    family = scope.get('id')
    modes = {'ui.popup': {'text-result'}, 'ui.notification': {'completion-only'},
             'clipboard.read-text': {'direct', 'manager-recipe'}, 'clipboard.write-text': {'explicit-copy'}}
    if family in modes:
        valid = set(scope) == {'id', 'mode'} and scope['mode'] in modes[family]
    elif family in ('vault.read-secret', 'vault.write-secret', 'vault.read-location'):
        valid = (set(scope) == {'id', 'field', 'entry_kind', 'purpose'}
                 and _identifier(scope['field'], 128) and scope['entry_kind'] in _PAIRS['location' if family == 'vault.read-location' else 'secret']
                 and _purpose(scope['purpose']))
    elif family in ('network.http','network.https') and scope.get('recipe') in ('manager-webhook','plugin-webhook'):
        origin=scope.get('origin','')
        valid = (set(scope) == {'id','origin','recipe','data_class'} and scope['data_class']=='example.ping'
                 and (_loopback_origin(origin) if family=='network.http' else origin=='https://127.0.0.1:39481'))
    elif family in ('network.https', 'vault.access'):
        recipes = {'clipboard-summary': ('api.example.test', 'clipboard.text')}
        host, data_class = recipes.get(scope.get('recipe'), ('', ''))
        origin = scope.get('origin', '')
        valid = bool(host) and origin.isascii() and origin.lower() in ('https://' + host, 'https://' + host + ':443')
        if family == 'network.https':
            valid = valid and set(scope) == {'id', 'origin', 'recipe', 'data_class'} and scope['data_class'] == data_class
        else:
            valid = (valid and set(scope) == {'id', 'mode', 'field', 'operation', 'recipe', 'origin'}
                     and scope['mode'] in ('browse-metadata', 'use-entry') and _identifier(scope['field'], 128)
                     and scope['operation'] == 'authorize-request')
        scope['origin'] = 'https://' + host
    else:
        valid = False
    if not valid:
        raise ValueError('Unsupported Manager permission scope')
    return tuple(sorted(scope.items()))


def canonical_permissions(manifest):
    extension = manifest.get('StreamDockManager', {})
    if 'Permissions' not in extension:
        return {}
    value = extension['Permissions']
    if (not isinstance(value, dict) or set(value) != {'version', 'actions'} or type(value['version']) is not int
            or value['version'] != 1 or not isinstance(value['actions'], dict) or not 1 <= len(value['actions']) <= 128):
        raise ValueError('Invalid Manager permission declaration')
    actions = {item.get('UUID') for item in manifest.get('Actions', []) if isinstance(item, dict) and isinstance(item.get('UUID'), str)}
    result = {}
    for action, group in value['actions'].items():
        if (not _identifier(action, 256) or action not in actions or not isinstance(group, dict)
                or set(group) - {'purpose', 'required', 'optional'} or not _purpose(group.get('purpose'))):
            raise ValueError('Invalid Manager permission action')
        required, optional = group.get('required', []), group.get('optional', [])
        if not isinstance(required, list) or not isinstance(optional, list) or not 1 <= len(required) + len(optional) <= 64:
            raise ValueError('Invalid Manager permission count')
        required, optional = ({_canonical_scope(item) for item in scopes} for scopes in (required, optional))
        if required & optional:
            raise ValueError('Permission cannot be both required and optional')
        result[action] = {'required': required, 'optional': optional, 'purpose': group['purpose']}
    return result


def validate_setup_slots(manifest):
    """Admit only bounded logical descriptors; all selection and grants are host-owned."""
    extension = manifest.get('StreamDockManager', {})
    if not isinstance(extension, dict):
        raise ValueError('Invalid Manager manifest extension')
    descriptor = extension.get('Capabilities', {})
    if not isinstance(descriptor, dict) or extension.get('Permissions') != descriptor.get('permissions'):
        raise ValueError('Package permissions must match the executable capability descriptor')
    permissions = canonical_permissions(manifest)
    if 'SetupSlots' not in extension:
        return
    setup = extension['SetupSlots']
    if (not isinstance(setup, dict) or set(setup) != {'version', 'slots'}
            or type(setup['version']) is not int or setup['version'] != 1
            or not isinstance(setup['slots'], list) or not 1 <= len(setup['slots']) <= 64):
        raise ValueError('Invalid Manager SetupSlots version or slot inventory')
    actions = {item.get('UUID') for item in manifest.get('Actions', []) if isinstance(item, dict) and isinstance(item.get('UUID'), str)}
    pairs = set()
    fields = set()
    slot_types = {}
    for slot in setup['slots']:
        broker = isinstance(slot, dict) and slot.get('mode') == 'broker'
        keys = _KEYS | ({'recipe', 'origin', 'operation'} if broker else set())
        if not isinstance(slot, dict) or set(slot) != keys:
            raise ValueError('Invalid Manager setup slot fields')
        kind, entry_kind, slot_id, field, action = (slot[key] for key in ('kind', 'entry_kind', 'id', 'field', 'action'))
        purpose = slot['purpose']
        if (not isinstance(kind, str) or not isinstance(entry_kind, str)
                or entry_kind not in _PAIRS.get(kind, ())
                or (not broker and slot['mode'] != 'direct') or type(slot['required']) is not bool
                or not _identifier(slot_id, 128) or not _identifier(field, 128)
                or not _identifier(action, 256) or action not in actions
                or not _purpose(purpose)):
            raise ValueError('Invalid Manager setup slot selector, type or purpose')
        if broker:
            valid = (kind == 'secret' and entry_kind in ('api-key', 'bearer-token')
                         and slot['recipe'] == 'clipboard-summary' and slot['operation'] == 'authorize-request')
            group = permissions.get(action, {})
            scope = _canonical_scope({'id':'vault.access','mode':'use-entry','field':field,
                                     'recipe':slot['recipe'],'origin':slot['origin'],'operation':slot['operation']})
            if not valid or slot['origin'] != dict(scope)['origin'] or group.get('purpose') != purpose or scope not in group.get('required' if slot['required'] else 'optional', set()):
                raise ValueError('Broker setup requires an exact use-entry scope and purpose')
        elif kind in ('secret', 'location'):
            scope = _canonical_scope({'id': 'vault.read-' + kind, 'field': field, 'entry_kind': entry_kind, 'purpose': purpose})
            if scope not in permissions.get(action, {}).get('required' if slot['required'] else 'optional', set()):
                raise ValueError('Direct Vault setup requires a matching read permission declaration')
        pair = (slot_id, action)
        identity = (kind, entry_kind, field, slot['required'], slot['mode'], slot.get('recipe'), slot.get('origin'), slot.get('operation'), slot.get('fields'), slot.get('precision'), slot.get('recipient'))
        if pair in pairs or (action, field) in fields or slot_types.get(slot_id, identity) != identity:
            raise ValueError('Duplicate or conflicting Manager setup slot')
        fields.add((action, field))
        pairs.add(pair)
        slot_types[slot_id] = identity
