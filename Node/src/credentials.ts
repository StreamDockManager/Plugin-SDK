import type { Settings } from './protocol.js';
// Decide whether a field name denotes credential material
export function secret(
    settings: Settings,
    key: string,
    lookup: (name: string) => string | undefined,
): string {
    const raw = typeof settings[key] === 'string' ? settings[key] : '';
    const match = /^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$/.exec(raw);
    if (match) {
        const value = lookup(match[1]);
        if (!value) throw Error(`${key} environment reference is unset`);
        return value;
    }
    if (raw.includes('${')) throw Error(`${key} requires a whole-value environment reference`);
    return raw;
}
const sensitiveKey = /(password|secret|token|api[_-]key|webhook|authorization|cookie)/i;
export function secretSettings(settings: Settings): Settings {
    return Object.fromEntries(
        Object.entries(settings).filter(
            ([key, value]) => typeof value === 'string' && sensitiveKey.test(key),
        ),
    );
}
// Remove credential-bearing fields before exposing diagnostics
export function redact(
    error: unknown,
    settings: Settings,
    lookup: (name: string) => string | undefined = () => undefined,
    keys: string[] = [],
): Error {
    if (error && typeof (error as { safeError?: unknown }).safeError === 'function')
        return (error as { safeError: () => Error }).safeError();
    let text = error instanceof Error ? error.message : String(error);
    for (const [key, raw] of Object.entries(settings)) {
        if (typeof raw !== 'string' || (!keys.includes(key) && !sensitiveKey.test(key))) continue;
        let resolved = raw;
        try {
            resolved = secret(settings, key, lookup);
        } catch {}
        for (const value of [raw, resolved])
            if (value) {
                const escaped = encodeURIComponent(value).replace(
                    /[!'()*]/g,
                    (char) => '%' + char.charCodeAt(0).toString(16).toUpperCase(),
                );
                for (const encoded of [value, escaped, escaped.replace(/%20/g, '+')])
                    text = text.split(encoded).join('[redacted]');
            }
    }
    return new Error(text.replace(/https?:\/\/[^\s"<>]+/g, '[service URL]'));
}

export const safeError = redact;
