import type { Event } from './protocol.js';

export const OPENACTION_SHOW_SETTINGS_INTERFACE = 'showSettingsInterface';
export const OPENACTION_DID_RECEIVE_DEEP_LINK = 'didReceiveDeepLink';
export interface OpenActionSettingsInterfaceEvent extends Event {
    event: typeof OPENACTION_SHOW_SETTINGS_INTERFACE;
}
export interface OpenActionDeepLinkEvent extends Event {
    event: typeof OPENACTION_DID_RECEIVE_DEEP_LINK;
    payload: { url: string; [key: string]: unknown };
}
/** Opt-in notification only; this helper never opens a settings window. */
export function openActionSettingsRequested(
    event: Event,
): event is OpenActionSettingsInterfaceEvent {
    return event.event === OPENACTION_SHOW_SETTINGS_INTERFACE;
}
/** Return the untrusted opaque URL. Never decode/execute it automatically. */
export function openActionDeepLink(event: Event): string | undefined {
    if (event.event !== OPENACTION_DID_RECEIVE_DEEP_LINK) return undefined;
    if (
        !event.payload ||
        Array.isArray(event.payload) ||
        typeof event.payload.url !== 'string' ||
        event.payload.url.length === 0
    )
        throw new Error('OpenAction deep link requires a nonempty payload.url string');
    return event.payload.url;
}
