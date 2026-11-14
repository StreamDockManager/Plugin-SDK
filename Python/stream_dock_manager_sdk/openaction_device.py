"""Opt-in OpenDeck device-provider envelopes, pinned to host 3ddecc1.

No connection, hardware access, host inference, URI loading, or grants. The host
must separately authorize the manifest DeviceNamespace. Manager/VSD is unchanged.
"""
from __future__ import annotations

import json
from typing import Any, Literal, NotRequired, TypedDict, Union


class OpenActionDeviceInfo(TypedDict):
    id: str
    name: str
    rows: int
    columns: int
    encoders: int
    touchpoints: NotRequired[int]
    infobars: NotRequired[int]
    type: int


class OpenActionDeviceImage(TypedDict):
    event: Literal['setImage']
    device: str
    controller: str | None
    position: int | None
    image: str | None


class OpenActionDeviceBrightness(TypedDict):
    event: Literal['setBrightness']
    device: str
    brightness: int


OpenActionDeviceResponse = Union[OpenActionDeviceImage, OpenActionDeviceBrightness]


# Validate the integer-valued device coordinate or dimension
def _integer(value: Any, low: int, high: int) -> int:
    if type(value) is not int or not low <= value <= high:
        raise ValueError(f'expected integer in [{low}, {high}]')
    return value


class OpenActionDeviceProvider:
    """Explicit namespace selection is not authorization; builders never send."""

    # Validate and retain the device namespace
    def __init__(self, namespace: str):
        if not isinstance(namespace, str) or len(namespace) != 2 or not all(33 <= ord(char) <= 126 for char in namespace):
            raise ValueError('DeviceNamespace must be exactly two printable ASCII characters')
        self._namespace = namespace

    # Resolve a registered device or reject an unknown device identifier
    def _device(self, device: str) -> str:
        if not isinstance(device, str) or not device.startswith(self._namespace) or len(device.encode('utf-8')) > 256:
            raise ValueError('device ID must begin with DeviceNamespace')
        return device

    # Register device
    def register_device(self, info: OpenActionDeviceInfo) -> dict[str, Any]:
        if not isinstance(info, dict) or set(info) - {'id', 'name', 'rows', 'columns', 'encoders', 'touchpoints', 'infobars', 'type'}:
            raise ValueError('unsupported device metadata')
        if not isinstance(info.get('name'), str) or not 1 <= len(info['name'].encode('utf-8')) <= 1024:
            raise ValueError('device name must be a string')
        payload = {'id': self._device(info.get('id')), 'name': info['name']}
        for field in ('rows', 'columns', 'encoders', 'type'):
            payload[field] = _integer(info.get(field), 0, 255)
        for field in ('touchpoints', 'infobars'):
            payload[field] = _integer(info.get(field, 0), 0, 255)
        return {'event': 'registerDevice', 'payload': payload}

    # Return deregister device for open action device provider
    def deregister_device(self, device: str) -> dict[str, Any]:
        return {'event': 'deregisterDevice', 'payload': self._device(device)}

    # Return rerender images for open action device provider
    def rerender_images(self, device: str) -> dict[str, Any]:
        return {'event': 'rerenderImages', 'payload': self._device(device)}

    # Return press for open action device provider
    def _press(self, event: str, device: str, position: int) -> dict[str, Any]:
        return {'event': event, 'payload': {'device': self._device(device), 'position': _integer(position, 0, 255)}}

    # Return key down for open action device provider
    def key_down(self, device: str, position: int) -> dict[str, Any]:
        return self._press('keyDown', device, position)

    # Return key up for open action device provider
    def key_up(self, device: str, position: int) -> dict[str, Any]:
        return self._press('keyUp', device, position)

    # Return encoder down for open action device provider
    def encoder_down(self, device: str, position: int) -> dict[str, Any]:
        return self._press('encoderDown', device, position)

    # Return encoder up for open action device provider
    def encoder_up(self, device: str, position: int) -> dict[str, Any]:
        return self._press('encoderUp', device, position)

    # Send the encoder rotation event for the selected device control
    def encoder_change(self, device: str, position: int, ticks: int) -> dict[str, Any]:
        envelope = self._press('encoderChange', device, position)
        envelope['payload']['ticks'] = _integer(ticks, -32768, 32767)
        return envelope

    # Send the touchscreen press event for the selected device
    def touchscreen_press(self, device: str, position: int, x: int, y: int, hold: bool = False) -> dict[str, Any]:
        if type(hold) is not bool:
            raise ValueError('hold must be boolean')
        envelope = self._press('touchscreenPress', device, position)
        envelope['payload'].update(x=_integer(x, 0, 65535), y=_integer(y, 0, 65535), hold=hold)
        return envelope

    # Parse provider output only. Image strings stay opaque, never fetched
    def parse_response(self, raw: str) -> OpenActionDeviceResponse:
        """Parse provider output only. Image strings stay opaque, never fetched."""
        if not isinstance(raw, str) or len(raw) > 1048576 or len(raw.encode('utf-8')) > 1048576:
            raise ValueError('response exceeds 1 MiB or is not text')
        value = json.loads(raw)
        if not isinstance(value, dict):
            raise ValueError('expected response object')
        self._device(value.get('device'))
        if value.get('event') == 'setBrightness' and set(value) == {'event', 'device', 'brightness'}:
            _integer(value['brightness'], 0, 255)
            return value
        if value.get('event') == 'setImage' and set(value) <= {'event', 'device', 'controller', 'position', 'image'}:
            controller, position, image = (value.get(key) for key in ('controller', 'position', 'image'))
            if controller is None and position is None and image is None:
                pass  # Host clear_screen signal
            elif isinstance(controller, str) and (image is None or isinstance(image, str)):
                _integer(position, 0, 255)
            else:
                raise ValueError('invalid image response')
            return {'event': 'setImage', 'device': value['device'], 'controller': controller, 'position': position, 'image': image}
        raise ValueError('unsupported device response')
