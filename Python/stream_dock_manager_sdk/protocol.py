"""Protocol v1 types. Unknown host/event fields remain available through raw."""
from dataclasses import dataclass, field
import base64
import json
import time

CONTRACT_VERSION = 1
DESCRIPTOR_LIMIT = 64 << 10
CAPABILITIES = (
    'plugin-protocol-v1', 'manager-controls-v1', 'telemetry-v1',
    'surface-metadata-v1', 'plugin-theme-v1', 'plugin-settings-v1',
    'desktop-input-v1', 'action-results-v1', 'network-payload-v1', 'plugin-logs-v1',
)
FEATURES = {
    'subscribeTelemetry': 'telemetry-v1', 'setDynamicItems': 'plugin-settings-v1',
    'desktopInput': 'desktop-input-v1', 'actionResult': 'action-results-v1',
    'networkPayload': 'network-payload-v1',
}


class UnsupportedFeatureError(RuntimeError):
    # Retain the unsupported feature name and format its error message
    def __init__(self, feature):
        self.feature = feature
        super().__init__('host does not support ' + feature)


@dataclass(frozen=True)
class HostInfo:
    raw: dict = field(default_factory=dict)

    # Decode host metadata and require a JSON object
    @classmethod
    def parse(cls, value):
        raw = json.loads(value) if isinstance(value, (str, bytes)) else value
        if not isinstance(raw, dict):
            raise ValueError('host info must be a JSON object')
        return cls(raw)

    # Expose the manager value from host info
    @property
    def manager(self):
        value = self.raw.get('streamDockManager', {})
        if (not isinstance(value, dict) or not isinstance(value.get('capabilities', []), list)
                or any(not isinstance(item, str) for item in value.get('capabilities', []))
                or type(value.get('contract_version')) is not int):
            return {}
        return value

    # Check whether the host advertises the requested capability
    def supports(self, feature):
        return type(self.manager.get('contract_version')) is int and self.manager.get('contract_version') == 1 and feature in self.manager.get('capabilities', [])


@dataclass(frozen=True)
class Surface:
    control_id: str = ''
    width: int = 0
    height: int = 0
    feedback_target: str = ''
    request_id: str = ''
    touch: dict | None = None
    activation_token: str = field(default='', repr=False)

    # Construct surface metadata from the recognized dataclass fields
    @classmethod
    def parse(cls, raw):
        return cls(**{key: raw[key] for key in cls.__dataclass_fields__ if key in raw})


@dataclass(frozen=True)
class Event:
    event: str
    context: str = ''
    action: str = ''
    device: str = ''
    payload: object = field(default_factory=dict)
    surface: Surface | None = None
    raw: dict = field(default_factory=dict)

    # Decode host events and validate payload shapes used by typed helpers
    @classmethod
    def parse(cls, value):
        raw = json.loads(value) if isinstance(value, (str, bytes)) else value
        if not isinstance(raw, dict) or not isinstance(raw.get('event'), str):
            raise ValueError('event must have a string event name')
        payload = raw.get('payload')
        arbitrary_payload = raw['event'] in ('sendToPlugin', 'sendToPropertyInspector')
        if payload is None and not arbitrary_payload:
            payload = {}
        if not isinstance(payload, dict) and not arbitrary_payload:
            if raw['event'] not in ('didReceiveSettings', 'didReceiveGlobalSettings'):
                raise ValueError('event payload must be an object')
            # Preserve the wire value in raw for the control waiter while keeping
            # ordinary event helpers safe on a malformed settings notification
            payload = {}
        if raw["event"] == "didReceiveTelemetry": TelemetrySnapshot(payload)
        surface = raw.get('streamDockManager')
        if surface is not None and not isinstance(surface, dict):
            raise ValueError('surface metadata must be an object')
        return cls(raw['event'], raw.get('context', ''), raw.get('action', ''), raw.get('device', ''),
                   payload, Surface.parse(surface) if surface is not None else None, raw)

    # Expose the settings value from event
    @property
    def settings(self):
        return self.payload.get('settings', {}) if isinstance(self.payload, dict) else {}

    # Expose the pressed value from event
    @property
    def pressed(self):
        return bool(self.payload.get('pressed', self.payload.get('hold', False))) if isinstance(self.payload, dict) else False

    # Expose the device info value from event
    @property
    def device_info(self):
        return self.raw.get('deviceInfo')


@dataclass(frozen=True)
class Measurement:
    value: float | None = None
    observed_at: int = 0
    max_age_ms: int = 0
    error: str | None = None

    # Check whether the measurement remains usable at the current time
    def fresh(self, now_ms=None):
        import math
        now_ms = int(time.time() * 1000) if now_ms is None else now_ms
        return (self.value is not None and self.error is None and math.isfinite(self.value)
                and self.observed_at <= now_ms <= self.observed_at + self.max_age_ms)


@dataclass(frozen=True)
class TelemetrySnapshot:
    raw: dict

    # Validate and normalize telemetry snapshot after construction
    def __post_init__(self):
        if not isinstance(self.raw, dict) or type(self.raw.get("audio_observer")) is not bool:
            raise ValueError("telemetry audio_observer must be a boolean")

    # Read and validate a measurement from the telemetry snapshot
    def measurement(self, topic):
        value = self.raw.get(topic, {})
        return Measurement(**{key: value[key] for key in Measurement.__dataclass_fields__ if key in value})

    # Expose the audio observer value from telemetry snapshot
    @property
    def audio_observer(self):
        return self.raw['audio_observer']

    # Expose the disks value from telemetry snapshot
    @property
    def disks(self):
        return {key: Measurement(**value) for key, value in self.raw.get('disks', {}).items()}


# Serialize the capability descriptor into a bounded native frame
def frame_capabilities(value):
    raw = json.dumps(value, separators=(',', ':'), ensure_ascii=False).encode()
    if len(raw) > DESCRIPTOR_LIMIT:
        raise ValueError('descriptor exceeds limit')
    encoded = base64.b64encode(raw).decode()
    return f'SDMCAP1:{len(encoded):08x}:{encoded}:ENDSDMCAP1'


# Parse capabilities
def parse_capabilities(frame):
    if not frame.startswith('SDMCAP1:') or len(frame) < 17:
        raise ValueError('invalid descriptor framing')
    try:
        size = int(frame[8:16], 16)
        if size > DESCRIPTOR_LIMIT * 2 or frame[16] != ':' or frame[17 + size:] != ':ENDSDMCAP1':
            raise ValueError('invalid descriptor length')
        raw = base64.b64decode(frame[17:17 + size], validate=True)
        if len(raw) > DESCRIPTOR_LIMIT:
            raise ValueError('descriptor exceeds limit')
        value = json.loads(raw)
        if not isinstance(value, dict):
            raise ValueError('descriptor must be an object')
        return value
    except (ValueError, TypeError) as error:
        raise ValueError('invalid capability descriptor') from error
