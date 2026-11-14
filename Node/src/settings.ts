import { validateSettings } from './internal/settings-validation.js';
import type { Settings } from './protocol.js';
export interface Field {
    path_picker?: 'file' | 'folder' | 'save';
    key: string;
    label?: string;
    type: string;
    default?: any;
    required?: boolean;
    sensitive?: boolean;
    options?: string[];
    option_labels?: Record<string, string>;
    min?: number;
    max?: number;
}
// Merge settings without mutating the supplied layers
export function merge(global: Settings, action: Settings): Settings {
    return { ...structuredClone(global), ...structuredClone(action) };
}
/** Internal connector projection: shared keys come only from plugin settings. */
export function project(
    global: Settings,
    action: Settings,
    sharedKeys: readonly string[] = [],
): Settings {
    const out = merge(global, action);
    for (const key of sharedKeys)
        if (Object.hasOwn(global, key)) out[key] = structuredClone(global[key]);
        else delete out[key];
    return out;
}
// Read a string setting with its fallback
export function string(s: Settings, key: string, fallback = ''): string {
    return typeof s[key] === 'string' && s[key] ? s[key] : fallback;
}
// Read a finite numeric setting with its fallback
export function number(s: Settings, key: string, fallback = 0): number {
    const raw = s[key],
        value =
            typeof raw === 'number'
                ? raw
                : typeof raw === 'string' &&
                    /^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$/.test(raw.trim())
                  ? Number(raw)
                  : NaN;
    return Number.isFinite(value) ? value : fallback;
}
// Read a boolean setting with its fallback
export function boolean(s: Settings, key: string): boolean {
    return s[key] === true;
}
// Validate settings against the supplied field definitions
export function validate(fields: Field[], settings: Settings): Settings {
    return validateSettings(fields, settings);
}
