"""Plugin SDK for Stream Dock Manager with OpenDeck (OpenAction) export and protocol features shared with VSD Craft and Elgato Stream Deck."""
from .protocol import (CAPABILITIES, CONTRACT_VERSION, Event, HostInfo, Measurement,
                       Surface, TelemetrySnapshot, UnsupportedFeatureError,
                       frame_capabilities, parse_capabilities)
from .network import NetworkPayload, TrackedStream
from .runtime import Client, Completion, Handler, LaunchOptions, ProbePool, connect, defer_action, run

__version__ = '0.1.1'
__all__ = ['CAPABILITIES', 'CONTRACT_VERSION', 'Event', 'HostInfo', 'Measurement', 'Surface',
           'TelemetrySnapshot', 'UnsupportedFeatureError', 'frame_capabilities',
           'parse_capabilities', 'NetworkPayload', 'TrackedStream', 'Client', 'Completion',
           'Handler', 'LaunchOptions', 'ProbePool', 'connect', 'defer_action', 'run']

from .theme import PluginTheme, ThemeColors, ThemeMedia, THEME_PROTOCOL, THEME_EVENT
__all__ += ["PluginTheme", "ThemeColors", "ThemeMedia", "THEME_PROTOCOL", "THEME_EVENT"]

from .portable_image import image_data_uri, MAX_PORTABLE_IMAGE_BYTES
__all__ += ['image_data_uri', 'MAX_PORTABLE_IMAGE_BYTES']

from .openaction_events import (OPENACTION_SHOW_SETTINGS_INTERFACE,
                               OPENACTION_DID_RECEIVE_DEEP_LINK,
                               openaction_settings_requested, openaction_deep_link)
__all__ += ['OPENACTION_SHOW_SETTINGS_INTERFACE', 'OPENACTION_DID_RECEIVE_DEEP_LINK',
            'openaction_settings_requested', 'openaction_deep_link']

from .openaction_device import (OpenActionDeviceProvider, OpenActionDeviceInfo,
                                OpenActionDeviceImage, OpenActionDeviceBrightness,
                                OpenActionDeviceResponse)
__all__ += ["OpenActionDeviceProvider", "OpenActionDeviceInfo", "OpenActionDeviceImage",
            "OpenActionDeviceBrightness", "OpenActionDeviceResponse"]

from .vault import Vault, VaultLocation, VaultTextValue, VAULT_VALUES_PROTOCOL
__all__ += ['Vault', 'VaultLocation', 'VaultTextValue', 'VAULT_VALUES_PROTOCOL']

from .vault import VaultSecretValue, VAULT_SECRET_SHARING_PROTOCOL
__all__ += ['VaultSecretValue', 'VAULT_SECRET_SHARING_PROTOCOL']

from .permissions import Permissions, PERMISSIONS_PROTOCOL, HTTP_BROKER_PROTOCOL, NOTIFICATIONS_PROTOCOL, NotificationLevel

from .vault import VaultWriteReceipt, VAULT_WRITES_PROTOCOL
__all__ += ['VaultWriteReceipt', 'VAULT_WRITES_PROTOCOL']

from .permissions import VAULT_REFERENCES_PROTOCOL, VaultEntryPage, VaultEntryName, PermissionNotice, permission_notice
__all__ += ['VAULT_REFERENCES_PROTOCOL', 'VaultEntryPage', 'VaultEntryName']

from .vault import TEXT_REFERENCES_PROTOCOL
