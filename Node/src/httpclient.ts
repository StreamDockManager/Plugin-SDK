import { finiteJSONValue } from './internal/json.js';
import { byteLength } from './internal/bytes.js';
import { startTrace } from './diagnostics.js';
import { createHash } from 'node:crypto';
import { request as http, Agent as HTTPAgent } from 'node:http';
import { request as https, Agent as HTTPSAgent } from 'node:https';
import { gunzipSync, inflateSync, brotliDecompressSync } from 'node:zlib';
import { retryAfter } from './polling.js';
import { redact as redactError } from './credentials.js';
import type { Settings } from './protocol.js';
import type { NetworkPayload } from './network.js';
export class HTTPError extends Error {
    // Remove credentials from HTTP failure details
    safeError(): HTTPError {
        return new HTTPError(this.status, this.retryAt);
    }
    // Attach HTTP status and retry metadata to the failure
    constructor(
        readonly status: number,
        readonly retryAt?: number,
    ) {
        super(`Service returned HTTP ${status}; check credentials, permissions and selection`);
    }
}
export class Service {
    private agents = new Map<string, HTTPAgent>();
    // Configure request deadlines, response limits and network accounting
    constructor(
        readonly limit = 4 << 20,
        readonly timeout = 10000,
        readonly network?: NetworkPayload,
    ) {}
    // Execute a bounded HTTP request with cancellation
    async do(
        method: string,
        endpoint: string,
        headers: Record<string, string> = {},
        body = '',
        signal?: AbortSignal,
        acceptStatus = false,
    ): Promise<{ status: number; headers: Record<string, string>; body: Uint8Array }> {
        const url = new URL(endpoint);
        if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.hash)
            throw Error('Invalid service endpoint');
        let agent = this.agents.get(url.protocol);
        if (!agent) {
            agent = new (url.protocol === 'https:' ? HTTPSAgent : HTTPAgent)({
                keepAlive: true,
                maxSockets: 4,
                maxFreeSockets: 4,
            });
            this.agents.set(url.protocol, agent);
        }
        const cancellation = AbortSignal.any([
            AbortSignal.timeout(this.timeout),
            ...(signal ? [signal] : []),
        ]);
        const trace = startTrace(this.network, method, url.href, headers);
        return new Promise<{ status: number; headers: Record<string, string>; body: Uint8Array }>(
            (resolve, reject) => {
                const req = (url.protocol === 'https:' ? https : http)(
                    url,
                    { method, headers, signal: cancellation, agent },
                    // Collect the HTTP response while enforcing trace and body limits
                    (res) => {
                        trace?.received(res.headers);
                        const chunks: Buffer[] = [];
                        let size = 0;
                        const encoding = res.headers['content-encoding'];
                        const compressed = ['gzip', 'deflate', 'br'].includes(String(encoding));
                        res.on(
                            'data',
                            // Account for each response chunk before retaining it under the byte limit
                            (chunk: Buffer) => {
                                size += chunk.length;
                                if (compressed) trace?.response.omit(chunk.length);
                                else trace?.response.write(chunk);
                                this.network?.add(0, chunk.length);
                                if (size > this.limit) {
                                    res.destroy(Error('Invalid or oversized service response'));
                                    return;
                                }
                                chunks.push(chunk);
                            },
                        );
                        res.on('error', (error) => {
                            trace?.finish(res.statusCode ?? 0, true);
                            reject(error);
                        });
                        res.on('end', () => {
                            try {
                                let data = Buffer.concat(chunks);
                                if (encoding === 'gzip')
                                    data = gunzipSync(data, { maxOutputLength: this.limit });
                                else if (encoding === 'deflate')
                                    data = inflateSync(data, { maxOutputLength: this.limit });
                                else if (encoding === 'br')
                                    data = brotliDecompressSync(data, {
                                        maxOutputLength: this.limit,
                                    });
                                if (data.length > this.limit)
                                    throw Error('Invalid or oversized service response');
                                const status = res.statusCode ?? 0;
                                if (trace) {
                                    if (compressed) {
                                        trace.received(res.headers);
                                        trace.response.write(data);
                                    }
                                    trace.response.complete = true;
                                    trace.finish(status);
                                }
                                if (!acceptStatus && (status < 200 || status >= 300))
                                    throw new HTTPError(
                                        status,
                                        retryAfter(String(res.headers['retry-after'] ?? '')),
                                    );
                                resolve({
                                    status,
                                    headers: Object.fromEntries(
                                        Object.entries(res.headers).map(([k, v]) => [
                                            k,
                                            String(v ?? ''),
                                        ]),
                                    ),
                                    body: data,
                                });
                            } catch (error) {
                                trace?.finish(res.statusCode ?? 0, true);
                                reject(error);
                            }
                        });
                    },
                );
                req.on('error', (error) => {
                    trace?.finish(0, true);
                    reject(cancellation.aborted ? cancellation.reason : redactError(error, {}));
                });
                req.on('finish', () => {
                    if (trace) {
                        trace.sent(
                            Object.fromEntries(
                                Object.entries(req.getHeaders()).map(([key, value]) => [
                                    key,
                                    Array.isArray(value) ? value : String(value ?? ''),
                                ]),
                            ),
                        );
                        trace.request.write(Buffer.from(body));
                        trace.request.complete = true;
                    }
                });
                this.network?.add(byteLength(body), 0);
                req.end(body);
            },
        ).finally(() => trace?.finish(0, true));
    }
    // Read and decode a bounded JSON response
    async json<T = Settings>(
        method: string,
        endpoint: string,
        headers: Record<string, string> = {},
        body?: unknown,
        signal?: AbortSignal,
    ): Promise<T | null> {
        const response = await this.do(
            method,
            endpoint,
            body === undefined ||
                Object.keys(headers).some((key) => key.toLowerCase() === 'content-type')
                ? headers
                : { 'Content-Type': 'application/json', ...headers },
            body === undefined
                ? ''
                : JSON.stringify(body, finiteJSONValue('Invalid JSON request number')),
            signal,
        );
        try {
            return response.body.length
                ? JSON.parse(new TextDecoder().decode(response.body))
                : null;
        } catch {
            throw Error('Malformed service response');
        }
    }
    // Cancel owned HTTP work and prevent further requests
    close(): void {
        for (const agent of this.agents.values()) agent.destroy();
        this.agents.clear();
    }
}
// Parse the supported response-selection path syntax
export function parsePath(path: string): { key: string; index?: number }[] {
    if (byteLength(path) > 8192) throw Error('Path exceeds 8192 bytes');
    const parts: { key: string; index?: number }[] = [];
    let i = 0;
    while (i < path.length) {
        const bracket = path[i] === '[';
        if (bracket) i++;
        if (i >= path.length) throw Error('Unclosed bracket');
        const quote = '"\''.includes(path[i]) ? path[i++] : '';
        let key = '';
        if (quote) {
            let closed = false;
            while (i < path.length) {
                let ch = path[i++];
                if (ch === quote) {
                    closed = true;
                    break;
                }
                if (ch === '\\') {
                    if (i >= path.length) throw Error('Unfinished escape');
                    ch = path[i++];
                    if (quote === '\"' && !'unrtbf\\/"'.includes(ch))
                        throw Error('Invalid JSON escape');
                    if (ch === 'u') {
                        const code = path.slice(i, i + 4);
                        if (!/^[a-fA-F0-9]{4}$/.test(code)) throw Error('Invalid unicode escape');
                        ch = String.fromCharCode(parseInt(code, 16));
                        i += 4;
                    } else ch = ({ n: '\n', r: '\r', t: '\t' } as Record<string, string>)[ch] ?? ch;
                }
                key += ch;
            }
            if (!closed) throw Error('Unterminated quoted key');
        } else {
            while (i < path.length && !'.[]'.includes(path[i])) {
                let ch = path[i++];
                if (ch === '\\') {
                    if (i >= path.length) throw Error('Unfinished escape');
                    ch = path[i++];
                }
                key += ch;
            }
            if (!key) throw Error('Empty component');
        }
        const index = !quote && /^\d+$/.test(key) ? Number(key) : undefined;
        if (bracket && !quote && index === undefined)
            throw Error('Brackets require index or quoted key');
        parts.push({ key, index });
        if (parts.length > 128) throw Error('Too many components');
        if (bracket && path[i++] !== ']') throw Error('Unclosed bracket');
        if (i === path.length) break;
        if (path[i] === '[') continue;
        if (path[i++] !== '.' || i === path.length || '.['.includes(path[i]))
            throw Error('Expected component');
    }
    return parts;
}
// Select a value from the response using a parsed path
export function extractPath(root: any, path: string): { value: any; found: boolean } {
    let value = root;
    for (const part of parsePath(path)) {
        if (Array.isArray(value) && part.index !== undefined && part.index < value.length)
            value = value[part.index];
        else if (
            value &&
            typeof value === 'object' &&
            !Array.isArray(value) &&
            Object.hasOwn(value, part.key)
        )
            value = value[part.key];
        else return { value: null, found: false };
    }
    return { value, found: true };
}
// Enumerate selectable paths from a sample response
export function pathSuggestions(root: any): string[] {
    const output: string[] = [];
    // Visit response fields while respecting traversal limits
    const walk = (value: any, path: string, depth: number) => {
        if (output.length >= 256 || depth > 12) return;
        output.push(path);
        if (Array.isArray(value))
            value.slice(0, 8).forEach((v, i) => walk(v, `${path}[${i}]`, depth + 1));
        else if (value && typeof value === 'object')
            for (const key of Object.keys(value).sort()) {
                const simple = !!key && !/[.\[\]\\"']/.test(key);
                walk(
                    value[key],
                    simple
                        ? (path ? path + '.' : '') + key
                        : path + '[' + JSON.stringify(key) + ']',
                    depth + 1,
                );
            }
    };
    walk(root, '', 0);
    return output;
}
// Identify response keys that contain secret values
function sensitive(key: string): boolean {
    return (
        ['key', 'signature'].includes(key.toLowerCase()) ||
        /token|password|secret|credential|authorization|cookie|api-key|api_key/i.test(key)
    );
}
// Sanitize response data before inspector exposure
export function redact(value: any, secrets: string[] = [], depth = 0): any {
    if (depth > 10) return '[preview depth limit]';
    if (Array.isArray(value)) return value.slice(0, 32).map((x) => redact(x, secrets, depth + 1));
    if (value && typeof value === 'object')
        return Object.fromEntries(
            Object.entries(value)
                .slice(0, 128)
                .map(([key, item]) => [
                    key,
                    sensitive(key) ? '[redacted]' : redact(item, secrets, depth + 1),
                ]),
        );
    if (typeof value === 'string') {
        for (const secret of [...new Set(secrets)].sort((a, b) => b.length - a.length))
            if (secret) value = value.split(secret).join('[redacted]');
        const points = Array.from(value);
        return points.slice(0, 4096).join('') + (points.length > 4096 ? '…' : '');
    }
    return value;
}
interface Entry {
    etag?: string;
    modified?: string;
    value: Settings;
    at: number;
    bytes: number;
    target: string;
}
export class ValidatorCache {
    readonly entries = new Map<string, Entry>();
    epoch = 0;
    // Discard response-derived state for the next operation
    reset(): void {
        this.epoch++;
        this.entries.clear();
    }
}
export interface RequestOptions {
    automatic?: boolean;
    cache?: ValidatorCache;
    signal?: AbortSignal;
    lookup?: (key: string) => string | undefined;
    network?: NetworkPayload;
    statusOnly?: boolean;
    noRedirect?: boolean;
    allowUserInfo?: boolean;
}
// Issue the configured HTTP operation
export async function request(settings: Settings, options: RequestOptions = {}): Promise<Settings> {
    const started = Date.now(),
        secrets: string[] = [];
    // Expand configured request placeholders from settings
    const expand = (raw: string) =>
        raw.replace(/\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)/g, (_m, a, b) => {
            const v = options.lookup?.(a ?? b);
            if (v === undefined) throw Error('Credential environment variable unavailable');
            secrets.push(v);
            return v;
        });
    // Inspect response data within the configured limits
    const scan = (value: any, key = '') => {
        if (Array.isArray(value)) value.forEach((x) => scan(x, key));
        else if (value && typeof value === 'object')
            Object.entries(value).forEach(([k, v]) => scan(v, k));
        else if (typeof value === 'string' && sensitive(key)) {
            secrets.push(value);
            if (key.toLowerCase() === 'authorization' && value.includes(' '))
                secrets.push(value.slice(value.indexOf(' ') + 1));
            if (key.toLowerCase() === 'cookie')
                value.split(';').forEach((part) => {
                    if (part.includes('=')) secrets.push(part.slice(part.indexOf('=') + 1).trim());
                });
        }
    };
    const method = String(settings.method ?? 'GET').toUpperCase();
    if (!['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'].includes(method))
        throw Error('Unsupported HTTP method');
    if (options.automatic && !['GET', 'HEAD'].includes(method))
        throw Error('Automatic requests must be read-only');
    let url = new URL(expand(settings.url ?? '')),
        body = expand(settings.body ?? '');
    const headers: Record<string, string> = Object.fromEntries(
        Object.entries(settings.headers ?? {}).map(([k, v]) => [k, expand(String(v))]),
    );
    if (
        !['http:', 'https:'].includes(url.protocol) ||
        !url.hostname ||
        ((url.username || url.password) && !options.allowUserInfo)
    )
        throw Error('Request URL must be HTTP(S) without embedded credentials');
    scan(settings);
    scan(headers);
    for (const [k, v] of url.searchParams) scan(v, k);
    try {
        scan(JSON.parse(body));
    } catch {}
    if (url.username || url.password) {
        secrets.push(decodeURIComponent(url.password));
        headers.Authorization =
            'Basic ' +
            Buffer.from(
                decodeURIComponent(url.username) + ':' + decodeURIComponent(url.password),
            ).toString('base64');
        url.username = '';
        url.password = '';
        scan(headers);
    }
    const timeout = Number(settings.timeout ?? 10);
    if (!Number.isFinite(timeout) || timeout <= 0 || timeout > 120)
        throw Error('Invalid request timeout');
    const fields = settings.fields ?? {};
    if (
        !fields ||
        typeof fields !== 'object' ||
        Array.isArray(fields) ||
        Object.keys(fields).length > 32
    )
        throw Error('Configure at most 32 named fields');
    for (const path of Object.values(fields)) parsePath(String(path));
    const cache = options.cache,
        epoch = cache?.epoch;
    const conditional = Object.keys(headers).some((key) =>
        /^(if-match|if-none-match|if-modified-since|if-unmodified-since|if-range|range)$/i.test(
            key,
        ),
    );
    const key = createHash('sha256')
        .update(
            JSON.stringify([
                method,
                url.href,
                headers,
                body,
                fields,
                settings.field,
                options.statusOnly,
                options.noRedirect,
                options.allowUserInfo,
                [...secrets].sort(),
            ]),
        )
        .digest('hex');
    const enabled =
        !!cache &&
        !!options.automatic &&
        method === 'GET' &&
        !conditional &&
        !Object.entries(headers).some(
            ([key, value]) => key.toLowerCase() === 'cache-control' && /no-store/i.test(value),
        );
    for (const [key, value] of cache?.entries ?? [])
        if (Date.now() - value.at > 1800000) cache!.entries.delete(key);
    const prior = enabled ? cache?.entries.get(key) : undefined;
    if (prior?.etag) headers['If-None-Match'] = prior.etag;
    else if (prior?.modified) headers['If-Modified-Since'] = prior.modified;
    const service = new Service(1 << 20, timeout * 1000, options.network);
    let response: Awaited<ReturnType<Service['do']>> | undefined,
        requestMethod = method;
    try {
        for (let redirects = 0; redirects < 5; redirects++) {
            response = await service.do(
                requestMethod,
                url.href,
                headers,
                body,
                options.signal,
                true,
            );
            if (
                ![301, 302, 303, 307, 308].includes(response.status) ||
                !response.headers.location ||
                options.noRedirect
            )
                break;
            const next = new URL(response.headers.location, url);
            if (next.origin !== url.origin || redirects === 4)
                throw Error('Cross-origin or excessive redirect');
            url = next;
            if (
                response.status === 303 ||
                ([301, 302].includes(response.status) && requestMethod === 'POST')
            ) {
                requestMethod = 'GET';
                body = '';
            }
        }
    } finally {
        service.close();
    }
    if (!response) throw Error('Request failed');
    const retryAt = retryAfter(response.headers['retry-after'] ?? '');
    // Report the response data retained for later selection
    const retained = (value: Settings) => {
        if (!cache || !enabled || epoch !== cache.epoch) return;
        cache.entries.delete(key);
        const etag = response!.headers.etag ?? prior?.etag,
            modified = response!.headers['last-modified'] ?? prior?.modified;
        if (
            /no-store/i.test(response!.headers['cache-control'] ?? '') ||
            (response!.headers.vary ?? '').split(',').some((x) => x.trim() === '*')
        )
            return;
        const validEtag =
                etag && etag.length <= 4096 && /^(W\/)?"[\x21\x23-\x7e\x80-\xff]*"$/.test(etag)
                    ? etag
                    : undefined,
            validModified =
                modified && Number.isFinite(Date.parse(modified)) ? modified : undefined;
        if (!validEtag && !validModified) return;
        const data = { ...structuredClone(value), preview: null, paths: [] },
            bytes =
                byteLength(JSON.stringify(data)) +
                String(etag ?? '').length +
                String(modified ?? '').length +
                64;
        if (bytes > 256 << 10) return;
        cache.entries.set(key, {
            etag: validEtag,
            modified: validModified,
            value: data,
            bytes,
            at: Date.now(),
            target: url.href,
        });
        while (
            cache.entries.size > 64 ||
            [...cache.entries.values()].reduce((n, x) => n + x.bytes, 0) > 8 << 20
        )
            cache.entries.delete(cache.entries.keys().next().value!);
    };
    if (options.automatic && method === 'GET' && response.status === 304) {
        const etag = response.headers.etag,
            modified = response.headers['last-modified'];
        if (
            !prior ||
            cache?.epoch !== epoch ||
            cache!.entries.get(key) !== prior ||
            prior.target !== url.href ||
            (etag &&
                (!/^(W\/)?"[\x21\x23-\x7e\x80-\xff]*"$/.test(etag) ||
                    (prior.etag && etag.replace(/^W\//, '') !== prior.etag.replace(/^W\//, '')))) ||
            (modified && !Number.isFinite(Date.parse(modified)))
        ) {
            cache?.entries.delete(key);
            throw Error('HTTP 304 has no matching retained response');
        }
        const value = {
            ...structuredClone(prior.value),
            status: 304,
            duration_ms: Date.now() - started,
            revalidated: true,
            retry_at: retryAt,
        };
        retained(value);
        return value;
    }
    const result: Settings = {
        status: response.status,
        duration_ms: Date.now() - started,
        fields: {},
        missing: [],
        preview: null,
        paths: [],
        revalidated: false,
        retry_at: retryAt,
    };
    if (!options.statusOnly && method !== 'HEAD' && response.body.length) {
        const text = new TextDecoder().decode(response.body).trimStart();
        let parsed: any;
        try {
            parsed = JSON.parse(text);
        } catch {
            if (/^(\{|\[|"|true|false|null|-?\d)/.test(text)) {
                // Reject multiple JSON values without treating ordinary text as structured data
                const candidates = [...text.matchAll(/(?<=\}|\]|"|\d)\s+(?=[{["\d])/g)];
                if (
                    candidates.some((match) => {
                        try {
                            JSON.parse(text.slice(0, match.index));
                            return true;
                        } catch {
                            return false;
                        }
                    })
                )
                    throw Error('Response contains trailing JSON data');
            }
            result.preview = redact(text.slice(0, 4096), secrets);
        }
        if (parsed !== undefined) {
            result.preview = redact(parsed, secrets);
            result.paths = pathSuggestions(parsed);
            for (const [name, path] of Object.entries(fields)) {
                const extracted = extractPath(parsed, String(path));
                if (extracted.found)
                    Object.defineProperty(result.fields, name, {
                        value: redact(extracted.value, secrets),
                        enumerable: true,
                        writable: true,
                    });
                else result.missing.push(name);
            }
        }
    }
    result.missing.sort();
    result.duration_ms = Date.now() - started;
    if (response.status === 200 || /no-store/i.test(response.headers['cache-control'] ?? ''))
        retained(result);
    return result;
}
