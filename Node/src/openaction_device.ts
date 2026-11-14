/** Opt-in OpenDeck 3ddecc1 device-provider envelopes. Never sends, accesses
 * hardware, fetches image URIs, or infers host authorization. Manager/VSD unchanged. */
export interface OpenActionDeviceInfo {
    id: string;
    name: string;
    rows: number;
    columns: number;
    encoders: number;
    touchpoints?: number;
    infobars?: number;
    type: number;
}
export interface OpenActionDeviceImage {
    event: 'setImage';
    device: string;
    controller: string | null;
    position: number | null;
    image: string | null;
}
export interface OpenActionDeviceBrightness {
    event: 'setBrightness';
    device: string;
    brightness: number;
}
export type OpenActionDeviceResponse = OpenActionDeviceImage | OpenActionDeviceBrightness;

// Validate an integer field against the device protocol bounds
function integer(value: unknown, low: number, high: number): number {
    if (typeof value !== 'number' || !Number.isInteger(value) || value < low || value > high)
        throw new TypeError(`expected integer in [${low}, ${high}]`);
    return value;
}

/** Namespace must also be authorized in the host manifest. Construction grants nothing. */
export class OpenActionDeviceProvider {
    readonly #namespace: string;
    // Bind provider messages to the host connection and plugin identity
    constructor(namespace: string) {
        if (typeof namespace !== 'string' || !/^[\x21-\x7e]{2}$/.test(namespace))
            throw new TypeError('DeviceNamespace must be exactly two printable ASCII characters');
        this.#namespace = namespace;
    }
    // Validate and normalize a provider device description
    private device(device: unknown): string {
        if (
            typeof device !== 'string' ||
            !device.startsWith(this.#namespace) ||
            Buffer.byteLength(device, 'utf8') > 256
        )
            throw new TypeError('device ID must begin with DeviceNamespace');
        return device;
    }
    // Announce a virtual device to the OpenAction host
    registerDevice(info: OpenActionDeviceInfo) {
        const allowed = [
            'id',
            'name',
            'rows',
            'columns',
            'encoders',
            'touchpoints',
            'infobars',
            'type',
        ];
        if (
            !info ||
            typeof info !== 'object' ||
            Object.keys(info).some((key) => !allowed.includes(key)) ||
            typeof info.name !== 'string' ||
            Buffer.byteLength(info.name, 'utf8') < 1 ||
            Buffer.byteLength(info.name, 'utf8') > 1024
        )
            throw new TypeError('unsupported device metadata');
        return {
            event: 'registerDevice' as const,
            payload: {
                id: this.device(info.id),
                name: info.name,
                rows: integer(info.rows, 0, 255),
                columns: integer(info.columns, 0, 255),
                encoders: integer(info.encoders, 0, 255),
                type: integer(info.type, 0, 255),
                touchpoints: integer(info.touchpoints === undefined ? 0 : info.touchpoints, 0, 255),
                infobars: integer(info.infobars === undefined ? 0 : info.infobars, 0, 255),
            },
        };
    }
    // Remove a previously registered provider device
    deregisterDevice(device: string) {
        return { event: 'deregisterDevice' as const, payload: this.device(device) };
    }
    // Request fresh rendered images for a provider device
    rerenderImages(device: string) {
        return { event: 'rerenderImages' as const, payload: this.device(device) };
    }
    // Send a provider control press with the requested event type
    private press<E extends string>(event: E, device: string, position: number) {
        return {
            event,
            payload: { device: this.device(device), position: integer(position, 0, 255) },
        };
    }
    // Report a key press for the provider device
    keyDown(device: string, position: number) {
        return this.press('keyDown', device, position);
    }
    // Report a key release for the provider device
    keyUp(device: string, position: number) {
        return this.press('keyUp', device, position);
    }
    // Report an encoder press for the provider device
    encoderDown(device: string, position: number) {
        return this.press('encoderDown', device, position);
    }
    // Report an encoder release for the provider device
    encoderUp(device: string, position: number) {
        return this.press('encoderUp', device, position);
    }
    // Report an encoder rotation for the provider device
    encoderChange(device: string, position: number, ticks: number) {
        const envelope = this.press('encoderChange', device, position);
        return {
            ...envelope,
            payload: { ...envelope.payload, ticks: integer(ticks, -32768, 32767) },
        };
    }
    // Report a touchscreen interaction for the provider device
    touchscreenPress(device: string, position: number, x: number, y: number, hold = false) {
        if (typeof hold !== 'boolean') throw new TypeError('hold must be boolean');
        const envelope = this.press('touchscreenPress', device, position);
        return {
            ...envelope,
            payload: {
                ...envelope.payload,
                x: integer(x, 0, 65535),
                y: integer(y, 0, 65535),
                hold,
            },
        };
    }
    /** Image content remains opaque; this method never fetches or decodes it. */
    parseResponse(raw: string): OpenActionDeviceResponse {
        if (
            typeof raw !== 'string' ||
            raw.length > 1048576 ||
            Buffer.byteLength(raw, 'utf8') > 1048576
        )
            throw new TypeError('response exceeds 1 MiB or is not text');
        const value = JSON.parse(raw) as Record<string, unknown>;
        if (!value || typeof value !== 'object' || Array.isArray(value))
            throw new TypeError('expected response object');
        const device = this.device(value.device);
        if (
            value.event === 'setBrightness' &&
            Object.keys(value).length === 3 &&
            'brightness' in value
        )
            return {
                event: 'setBrightness',
                device,
                brightness: integer(value.brightness, 0, 255),
            };
        if (
            value.event === 'setImage' &&
            Object.keys(value).every((key) =>
                ['event', 'device', 'controller', 'position', 'image'].includes(key),
            )
        ) {
            const controller = value.controller ?? null,
                position = value.position ?? null,
                image = value.image ?? null;
            if (controller === null && position === null && image === null)
                return { event: 'setImage', device, controller, position, image };
            if (typeof controller === 'string' && (image === null || typeof image === 'string'))
                return {
                    event: 'setImage',
                    device,
                    controller,
                    position: integer(position, 0, 255),
                    image,
                };
        }
        throw new TypeError('unsupported device response');
    }
}
