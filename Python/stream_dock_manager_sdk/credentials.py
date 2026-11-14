"""Explicit credential resolution and safe diagnostic messages; no work on import."""
import os
import re
from urllib.parse import quote, quote_plus


# Mark or detect a credential value for redaction
def secret(settings, key, lookup=os.environ.get):
    value = settings.get(key, '')
    if not isinstance(value, str): value = ''
    match = re.fullmatch(r'\$\{([A-Za-z_][A-Za-z0-9_]*)\}', value)
    if match:
        value = lookup(match[1])
        if not value: raise ValueError(f'{key} environment reference is unset')
    elif '${' in value: raise ValueError(f'{key} requires a whole-value environment reference')
    return value


# Format an error without exposing configured credentials
def safe_error(error, settings, sensitive_keys=(), lookup=os.environ.get):
    if error is None: return None
    if hasattr(error, 'safe_error'): return error.safe_error()
    text = str(error)
    for key, raw in settings.items():
        if not isinstance(raw, str) or not (key in sensitive_keys or re.search(r'password|secret|token|api[_-]key|webhook|authorization|cookie', key, re.I)): continue
        try: resolved = secret(settings, key, lookup)
        except ValueError: resolved = ''
        for value in (raw, resolved):
            if value:
                for variant in (value, quote(value, safe=''), quote_plus(value)): text = text.replace(variant, '[redacted]')
    return RuntimeError(re.sub(r'https?://[^\s"<>]+', '[service URL]', text))
