export type Json = null | boolean | number | string | Json[] | { [key: string]: Json };
export type Settings = Record<string, any>;
export interface Event {
    event: string;
    id?: string;
    context?: string;
    action?: string;
    device?: string;
    // Vendor inspector messages can carry any JSON value, not just an object
    payload?: any;
    streamDockManager?: Settings;
    protocol_id?: string;
    request_id?: string;
    method?: string;
    result?: any;
    error?: { code: string; message: string };
}
export const MAX_MESSAGE = 1 << 20;
const encoder = new TextEncoder();
export const FEATURES: Record<string, string> = {
    desktopInput: 'desktop-input-v1',
    subscribeTelemetry: 'telemetry-v1',
    actionResult: 'action-results-v1',
    setDynamicItems: 'plugin-settings-v1',
    networkPayload: 'network-payload-v1',
};
export class UnsupportedFeatureError extends Error {
    // Name the capability missing from the connected host
    constructor(public feature: string) {
        super(`Host does not support ${feature}`);
    }
}
export class HostInfo {
    // Retain host identity and its advertised capability set
    constructor(public raw: Settings = {}) {
        if (!raw || typeof raw !== 'object' || Array.isArray(raw))
            throw Error('Host info must be an object');
    }
    // Test whether the host advertised the requested capability
    supports(feature: string): boolean {
        const manager = this.raw.streamDockManager;
        return (
            manager !== null &&
            typeof manager === 'object' &&
            !Array.isArray(manager) &&
            manager.contract_version === 1 &&
            Array.isArray(manager.capabilities) &&
            manager.capabilities.every((value: unknown) => typeof value === 'string') &&
            manager.capabilities.includes(feature)
        );
    }
}
// Decode and validate a host event
export function parseEvent(value: string | Event): Event {
    if (typeof value === 'string' && encoder.encode(value).length > MAX_MESSAGE)
        throw Error('SDK message exceeds 1048576-byte UTF-8 JSON limit');
    const event = typeof value === 'string' ? JSON.parse(value) : structuredClone(value);
    if (
        !event ||
        typeof event.event !== 'string' ||
        (event.context != null && typeof event.context !== 'string') ||
        (event.payload != null &&
            (typeof event.payload !== 'object' || Array.isArray(event.payload)) &&
            event.event !== 'didReceiveSettings' &&
            event.event !== 'didReceiveGlobalSettings' &&
            event.event !== 'sendToPlugin' &&
            event.event !== 'sendToPropertyInspector')
    )
        throw Error('Invalid event');
    if (event.event === 'didReceiveTelemetry' && typeof event.payload?.audio_observer !== 'boolean')
        throw Error('Telemetry audio_observer must be a boolean');
    return event;
}
// Create an independent normalized protocol value
export function fresh(measurement: Settings, now: number): boolean {
    return (
        Number.isFinite(measurement?.value) &&
        measurement.error == null &&
        Number.isFinite(measurement.observed_at) &&
        measurement.observed_at >= 0 &&
        Number.isFinite(measurement.max_age_ms) &&
        measurement.max_age_ms >= 0 &&
        now >= measurement.observed_at &&
        now - measurement.observed_at <= measurement.max_age_ms
    );
}
// Extract capabilities from the startup frame
export function frameCapabilities(value: Settings): string {
    const bytes = encoder.encode(JSON.stringify(value));
    if (bytes.length > 65536) throw Error('Descriptor exceeds limit');
    const data = btoa(Array.from(bytes, (x) => String.fromCharCode(x)).join(''));
    return `SDMCAP1:${data.length.toString(16).padStart(8, '0')}:${data}:ENDSDMCAP1`;
}

// Validate the advertised host capabilities
export function parseCapabilities(frame: string): Settings {
    const match = /^SDMCAP1:([0-9a-fA-F]{8}):([A-Za-z0-9+/]*={0,2}):ENDSDMCAP1$/.exec(frame);
    if (!match || match[2].length !== parseInt(match[1], 16) || match[2].length > 131072)
        throw Error('Invalid capability descriptor');
    const bytes = Uint8Array.from(atob(match[2]), (char) => char.charCodeAt(0));
    if (bytes.length > 65536) throw Error('Descriptor exceeds limit');
    const value = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes));
    if (!value || typeof value !== 'object' || Array.isArray(value))
        throw Error('Descriptor must be an object');
    return value;
}
