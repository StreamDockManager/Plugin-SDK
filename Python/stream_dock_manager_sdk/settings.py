"""Declarative field validation. Unknown JSON keys are retained by deep copy."""
from copy import deepcopy
import math


# Merge settings data
def merge(global_settings, action_settings):
    return {**deepcopy(global_settings), **deepcopy(action_settings)}


# Detached connector settings with shared keys owned exclusively by globals
def project(global_settings, action_settings, shared_keys=()):
    """Detached connector settings with shared keys owned exclusively by globals."""
    result = merge(global_settings, action_settings)
    for key in shared_keys:
        if key in global_settings: result[key] = deepcopy(global_settings[key])
        else: result.pop(key, None)
    return result


# Validate settings inputs before further processing
def validate(fields, settings):
    if not isinstance(settings, dict): raise ValueError('settings must be an object')
    if len(fields)>256: raise ValueError('too many settings fields')
    result, seen = deepcopy(settings), set()
    for field in fields:
        key, kind = field.get('key'), field.get('type')
        if not isinstance(key, str) or not key or key in seen:
            raise ValueError('empty or duplicate settings field')
        seen.add(key)
        picker = field.get('path_picker', '')
        if picker and (kind != 'path' or picker not in ('file', 'folder', 'save')): raise ValueError('invalid path picker')
        if field.get('sensitive') and kind in ('number', 'checkbox', 'pairs'):
            raise ValueError(f'{key}: sensitive requires a string field')
        if kind not in ('text','textarea','path','device','select','checkbox','number','pairs'): raise ValueError(f'{key}: unknown field type {kind}')
        low, high = field.get('min'), field.get('max')
        if any(value is not None and (type(value) not in (int, float) or not math.isfinite(value)) for value in (low, high)) or low is not None and high is not None and low > high:
            raise ValueError(f'{key}: invalid bounds')
        if key not in result:
            if 'default' in field:
                result[key] = deepcopy(field['default'])
            elif field.get('required'):
                raise ValueError(f'{key}: required')
            else:
                continue
        value = result[key]
        if kind in ('text', 'textarea', 'device', 'path', 'select'):
            valid = isinstance(value, str) and (not field.get('required') or bool(value))
            if kind == 'select': valid = valid and value in field.get('options', [])
        elif kind == 'number':
            valid = type(value) in (int, float) and math.isfinite(value) and (low is None or value >= low) and (high is None or value <= high)
        elif kind == 'checkbox': valid = type(value) is bool
        elif kind == 'pairs': valid = isinstance(value, dict) and all(isinstance(v, str) for v in value.values())
        else: raise ValueError(f'{key}: unknown field type {kind}')
        if not valid: raise ValueError(f'{key}: invalid {kind} value')
    return result


# Validate the string-valued settings field
def string(settings, key, fallback=''):
    value = settings.get(key)
    return value if isinstance(value, str) and value else fallback


# Validate the numeric settings field
def number(settings, key, fallback=0):
    value = settings.get(key)
    if type(value) in (int, float) and math.isfinite(value): return value
    if isinstance(value, str) and '_' not in value:
        try:
            parsed=float(value)
            return parsed if math.isfinite(parsed) else fallback
        except ValueError: pass
    return fallback


# Return boolean for settings
def boolean(settings, key): return settings.get(key) is True
