import { byteLength } from './internal/bytes.js';
// Session-owned, opt-in network diagnostics. No transports or globals are replaced
type Fields = Record<string, string | string[] | undefined>;
type Sink = (payload: Record<string, unknown>, failed: boolean) => void;
const encoder = new TextEncoder();
const decoder = new TextDecoder();
const BODY = 64 << 10,
    HEADER = 8 << 10;
// Normalize a header or field name for privacy checks
const normalized = (key: string) => key.toLowerCase().replace(/[^a-z0-9]/g, '');
// Identify headers that must not enter diagnostics
export const sensitiveHeader = (key: string): boolean => {
    key = normalized(key);
    if (['authority', 'accesscontrolallowcredentials'].includes(key)) return false;
    return (
        /(auth|cookie|token|secret|password|credential|signature|apikey|csrf|xsrf|hmac|bearer|sessionid)/.test(
            key,
        ) || /(?:key|session)$/.test(key)
    );
};
// Identify fields whose values require redaction
const sensitive = (key: string) =>
    /(password|secret|token|api_?key|api-key|authorization|cookie|webhook|credential|signature)/i.test(
        key,
    );
// Produce a credential-free endpoint description
function endpoint(raw: string): string {
    try {
        const url = new URL(raw, 'http://relative.invalid');
        url.username = '';
        url.password = '';
        url.hash = '';
        for (const key of [...url.searchParams.keys()]) url.searchParams.set(key, '[redacted]');
        return raw.startsWith('/') || raw.startsWith('?')
            ? url.pathname + url.search
            : url.toString();
    } catch {
        return '[endpoint]';
    }
}
// Redact URL-valued diagnostic headers
function headerURL(name: string, value: string): string {
    name = normalized(name);
    if (
        /(url|uri)$/.test(name) ||
        ['location', 'contentlocation', 'referer', 'destination'].includes(name) ||
        /^[/?]/.test(value)
    )
        return endpoint(value);
    return value.replace(/[a-z][a-z0-9+.-]*:\/\/[^\s"<>]+/gi, endpoint);
}
// Copy allowed headers within the diagnostic size limit
function boundedHeaders(headers: Fields): { fields: Fields; overflow: boolean } {
    const fields: Fields = {};
    let used = 0,
        overflow = false;
    for (const [name, raw] of Object.entries(headers)) {
        const values = Array.isArray(raw) ? raw : [raw ?? ''];
        const cost = byteLength(name) + values.reduce((n, value) => n + byteLength(value), 0);
        if (Object.keys(fields).length >= 128 || name.length > 256 || used + cost > BODY) {
            overflow = true;
            continue;
        }
        fields[name] = [...values];
        used += cost;
    }
    return { fields, overflow };
}
class Capture {
    private chunks: Uint8Array[] = [];
    bytes = 0;
    complete = false;
    truncated = false;
    // Initialize the bounded diagnostic capture buffer
    constructor(readonly contentType = '') {
        if (byteLength(contentType) > 256) this.contentType = '[content type omitted]';
    }
    // Append a bounded diagnostic record
    write(data: Uint8Array): void {
        this.bytes += data.length;
        if (this.bytes > BODY) this.omit(0);
        else if (!this.truncated) this.chunks.push(data.slice());
    }
    // Account for records excluded by diagnostic limits
    omit(bytes: number): void {
        this.bytes += bytes;
        this.truncated = true;
        this.chunks = [];
    }
    // Return the retained diagnostic records and counters
    snapshot(redact: (text: string) => string): Record<string, unknown> {
        const data = new Uint8Array(this.truncated ? 0 : this.bytes);
        let offset = 0;
        for (const chunk of this.chunks) {
            data.set(chunk, offset);
            offset += chunk.length;
        }
        let text = decoder.decode(data);
        const original = text;
        const binary =
            !encoder.encode(text).every((byte, index) => byte === data[index]) ||
            /[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/.test(text);
        try {
            // Sanitize a diagnostic value before retention
            const clean = (value: unknown, depth = 0): unknown => {
                if (depth > 64) return '[redacted: nesting limit]';
                if (Array.isArray(value)) return value.map((item) => clean(item, depth + 1));
                if (value && typeof value === 'object')
                    return Object.fromEntries(
                        Object.entries(value).map(([key, value]) => [
                            key,
                            sensitive(key) ? '[redacted]' : clean(value, depth + 1),
                        ]),
                    );
                return typeof value === 'string' ? redact(value) : value;
            };
            text = JSON.stringify(clean(JSON.parse(text)));
        } catch {
            if (text && (/^[\s]*[\[{]/.test(text) || /json/i.test(this.contentType))) {
                text = '';
                this.truncated = true;
            }
            if (this.contentType.toLowerCase().includes('application/x-www-form-urlencoded')) {
                const form = new URLSearchParams(text);
                for (const key of [...form.keys()]) if (sensitive(key)) form.set(key, '[redacted]');
                text = form.toString();
            }
        }
        text = redact(text);
        if (byteLength(text) > BODY) {
            text = '';
            this.truncated = true;
        }
        if (this.truncated) text = '';
        if (binary)
            text = Buffer.from(
                text === original && !this.truncated ? data : encoder.encode(text),
            ).toString('base64');
        return {
            text,
            encoding: binary ? 'base64' : 'utf8',
            content_type: redact(this.contentType).slice(0, 256),
            bytes: this.bytes,
            complete: this.complete,
            truncated: this.truncated,
        };
    }
}
// Create a bounded trace for one network operation
export function startTrace(
    network: import('./network.js').NetworkPayload | undefined,
    method: string,
    url: string,
    headers: Fields = {},
): NetworkTrace | undefined {
    return network?.diagnostics.trace(
        (id, sink, redact) => new NetworkTrace(id, method, url, headers, sink, redact),
    );
}
export class NetworkTrace {
    readonly request: Capture;
    response = new Capture();
    private requestHeaders: Fields;
    private responseHeaders: Fields = {};
    private started = performance.now();
    private finished = false;
    private headersOverflow = false;
    // Initialize request and response capture for one network operation
    constructor(
        readonly id: number,
        readonly method: string,
        readonly url: string,
        headers: Fields,
        private sink: Sink,
        private redact: (text: string) => string,
    ) {
        const captured = boundedHeaders(headers);
        this.requestHeaders = captured.fields;
        this.headersOverflow = captured.overflow;
        this.request = new Capture(
            String(headers['Content-Type'] ?? headers['content-type'] ?? ''),
        );
    }
    // Account for bytes sent by the traced operation
    sent(headers: Fields): void {
        const captured = boundedHeaders(headers);
        this.requestHeaders = captured.fields;
        this.headersOverflow ||= captured.overflow;
    }
    // Account for bytes received by the traced operation
    received(headers: Fields): void {
        const captured = boundedHeaders(headers);
        this.responseHeaders = captured.fields;
        this.headersOverflow ||= captured.overflow;
        this.response = new Capture(
            String(headers['Content-Type'] ?? headers['content-type'] ?? ''),
        );
    }
    // Finalize the operation trace with its outcome
    finish(status = 0, failed = false): void {
        if (this.finished) return;
        this.finished = true;
        try {
            const secrets = new Set<string>();
            let overflow = this.headersOverflow,
                secretBytes = 0;
            // Retain a completed trace under the configured budget
            const remember = (value: string) => {
                if (!value || secrets.has(value)) return;
                secretBytes += byteLength(value);
                if (secrets.size >= 256 || secretBytes > BODY) {
                    overflow = true;
                    return;
                }
                secrets.add(value);
            };
            for (const headers of [this.requestHeaders, this.responseHeaders])
                for (const [name, raw] of Object.entries(headers))
                    if (sensitiveHeader(name))
                        for (const value of Array.isArray(raw) ? raw : [raw ?? '']) {
                            remember(value);
                            if (/auth/i.test(name)) {
                                remember(value.replace(/^\S+\s+/, ''));
                                if (/^basic /i.test(value)) {
                                    try {
                                        const decoded = atob(value.slice(6));
                                        remember(decoded);
                                        for (const part of decoded.split(':')) remember(part);
                                    } catch {}
                                }
                                for (const match of value.matchAll(
                                    /(?:credential|username|response|nonce|cnonce)=["']?([^,"'\s]+)/gi,
                                ))
                                    remember(match[1]);
                            }
                            if (/cookie/i.test(name))
                                for (const pair of value.split(';'))
                                    remember(pair.slice(pair.indexOf('=') + 1).trim());
                        }
            const patterns = [...secrets]
                .flatMap((value) => [
                    value,
                    encodeURIComponent(value),
                    encodeURIComponent(value).replace(/%20/g, '+'),
                    JSON.stringify(value).slice(1, -1),
                ])
                .sort((a, b) => b.length - a.length);
            // Produce the safe representation of a trace field
            const redact = (text: string) => {
                if (overflow) return '[redacted: credential capture limit exceeded]';
                for (const pattern of patterns)
                    if (pattern) text = text.split(pattern).join('[redacted]');
                return this.redact(text);
            };
            // Remove sensitive headers from the recorded exchange
            const cleanHeaders = (headers: Fields) => {
                const entries: { name: string; values: string[] }[] = [];
                let used = 0,
                    truncated = overflow;
                for (const [name, raw] of Object.entries(headers).sort(([a], [b]) =>
                    a.localeCompare(b),
                )) {
                    if (entries.length >= 128 || byteLength(name) > 256) {
                        truncated = true;
                        continue;
                    }
                    const values: string[] = [];
                    used += byteLength(name) + 32;
                    for (const value of Array.isArray(raw) ? raw : [raw ?? '']) {
                        const clean = sensitiveHeader(name)
                            ? '[redacted]'
                            : redact(headerURL(name, value));
                        const cost = byteLength(clean) + 8;
                        if (used + cost > HEADER) {
                            truncated = true;
                            continue;
                        }
                        values.push(clean);
                        used += cost;
                    }
                    if (used > HEADER) {
                        truncated = true;
                        continue;
                    }
                    entries.push({ name: redact(name), values });
                }
                return { entries, truncated };
            };
            this.sink(
                {
                    message: redact(
                        `Request ${this.id} · ${this.method.replace(/[\x00-\x1f\x7f]/g, '')} ${endpoint(this.url)} · ${failed ? 'request failed or cancelled' : `HTTP ${status}`} · ${Math.floor(performance.now() - this.started)} ms`,
                    ),
                    network: {
                        request_id: String(this.id),
                        request: this.request.snapshot(redact),
                        response: this.response.snapshot(redact),
                        request_headers: cleanHeaders(this.requestHeaders),
                        response_headers: cleanHeaders(this.responseHeaders),
                    },
                },
                failed || status >= 400,
            );
        } catch {
            /* Diagnostic failures never change request results */
        }
    }
}
