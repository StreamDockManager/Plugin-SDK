"""Opt-in OpenAction notifications; no UI or URLs are opened automatically."""
from .protocol import Event

OPENACTION_SHOW_SETTINGS_INTERFACE = 'showSettingsInterface'
OPENACTION_DID_RECEIVE_DEEP_LINK = 'didReceiveDeepLink'


# Advertise HasSettingsInterface only when your handler supplies that UI
def openaction_settings_requested(event: Event) -> bool:
    """Advertise HasSettingsInterface only when your handler supplies that UI."""
    return event.event == OPENACTION_SHOW_SETTINGS_INTERFACE


# Return the untrusted opaque URL; caller owns validation before any action
def openaction_deep_link(event: Event) -> str | None:
    """Return the untrusted opaque URL; caller owns validation before any action."""
    if event.event != OPENACTION_DID_RECEIVE_DEEP_LINK:
        return None
    if (not isinstance(event.payload, dict) or
            not isinstance(event.payload.get('url'), str) or not event.payload['url']):
        raise ValueError('OpenAction deep link requires a nonempty payload.url string')
    return event.payload['url']
