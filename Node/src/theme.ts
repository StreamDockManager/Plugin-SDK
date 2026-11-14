import { byteLength } from './internal/bytes.js';

export const THEME_PROTOCOL = 'plugin-theme-v1';
export const THEME_EVENT = 'didReceivePluginTheme';
const colorRoles = ['surface', 'ink', 'muted', 'accent', 'meter', 'border'] as const;
export interface ThemeMedia {
    kind: 'media';
    asset_id: string;
    format: string;
    animated: boolean;
}
export type ThemeColors = Record<(typeof colorRoles)[number], string> & {
    wallpaper: string | ThemeMedia;
};
export interface PluginTheme {
    version: 1;
    resolved: 'light' | 'dark';
    revision: number;
    theme: ThemeColors;
}

// Decode a host theme into the SDK theme representation
export function decodePluginTheme(value: unknown): PluginTheme {
    const data = value as PluginTheme | null;
    if (
        !data ||
        data.version !== 1 ||
        !['light', 'dark'].includes(data.resolved) ||
        !Number.isSafeInteger(data.revision) ||
        data.revision < 0 ||
        !data.theme ||
        colorRoles.some(
            (key) =>
                typeof data.theme[key] !== 'string' || !/^#[0-9a-f]{6}$/i.test(data.theme[key]),
        )
    )
        throw Error('Invalid plugin theme');
    const wallpaper = data.theme.wallpaper;
    const valid =
        typeof wallpaper === 'string'
            ? wallpaper.length > 0 && byteLength(wallpaper) <= 64 * 1024
            : wallpaper &&
              wallpaper.kind === 'media' &&
              typeof wallpaper.asset_id === 'string' &&
              wallpaper.asset_id.length &&
              typeof wallpaper.format === 'string' &&
              wallpaper.format.length &&
              typeof wallpaper.animated === 'boolean';
    if (!valid) throw Error('Invalid plugin theme wallpaper');
    return data;
}
