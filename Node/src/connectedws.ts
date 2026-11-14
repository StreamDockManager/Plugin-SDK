/** Optional service WebSockets. Authentication and protocol envelopes remain caller-owned. */
import WebSocket from 'ws';
import { connect as tcp } from 'node:net';
import { connect as tls } from 'node:tls';
import { NetworkPayload } from './network.js';
export const MAX_FRAME = 4 << 20,
    MAX_REQUEST = 64 << 10;
// Create the error used when the service connection is unavailable
const closed = () => Error('WebSocket connection closed; reconnect to refresh state');
export class Rejected extends Error {
    // Represent a service command rejection with its protocol code
    constructor(operation: string) {
        super(operation + ' was rejected; check permissions and the selected resource');
    }
}
// Translate a service rejection into a command error
export function commandError(error: unknown): Error {
    return error instanceof Rejected
        ? error
        : Error('Command delivery is uncertain; check the server state before trying again', {
              cause: error,
          });
}
// Serialize a service message before admission
function encode(value: unknown): string {
    let text: string;
    try {
        text = JSON.stringify(value);
    } catch {
        throw Error('Invalid service request');
    }
    if (text === undefined || Buffer.byteLength(text) > MAX_REQUEST)
        throw Error('Invalid or oversized service request');
    return text;
}
export type Route = (message: unknown) => string | undefined;
/** Bounded JSON channel used only by the authentication callback. */
export class Channel {
    private queue: { value: unknown; size: number }[] = [];
    private bytes = 0;
    private waiter?: { resolve: (value: unknown) => void; reject: (error: Error) => void };
    private route?: (value: unknown) => void;
    // Initialize bounded framing and send-queue state for the connection
    constructor(readonly socket: WebSocket) {
        socket.on('error', () => this.close());
        socket.on('close', () => {
            this.waiter?.reject(closed());
            this.waiter = undefined;
        });
        socket.on('message', (raw, binary) => {
            try {
                const data = Array.isArray(raw)
                    ? Buffer.concat(raw)
                    : Buffer.from(raw as ArrayBuffer);
                if (binary || data.length > MAX_FRAME) throw Error('Invalid service frame');
                const value = JSON.parse(data.toString());
                if (this.route) this.route(value);
                else if (this.waiter) {
                    const waiter = this.waiter;
                    this.waiter = undefined;
                    waiter.resolve(value);
                } else {
                    if (this.queue.length >= 32 || this.bytes + data.length > MAX_FRAME)
                        throw Error('Service handshake queue full');
                    this.queue.push({ value, size: data.length });
                    this.bytes += data.length;
                }
            } catch {
                this.close();
            }
        });
    }
    // Encode and enqueue a bounded service message
    send(value: unknown): Promise<void> {
        try {
            return this.sendEncoded(encode(value));
        } catch (error) {
            return Promise.reject(error);
        }
    }
    // Enqueue an already encoded service frame
    sendEncoded(text: string): Promise<void> {
        if (Buffer.byteLength(text) > MAX_REQUEST)
            return Promise.reject(Error('Invalid or oversized service request'));
        return new Promise((resolve, reject) => {
            if (this.socket.readyState !== WebSocket.OPEN) {
                reject(closed());
                return;
            }
            this.socket.send(text, (error) => (error ? reject(closed()) : resolve()));
        });
    }
    // Deliver incoming frames through the active connection handler
    receive(): Promise<unknown> {
        if (this.queue.length) {
            const entry = this.queue.shift()!;
            this.bytes -= entry.size;
            return Promise.resolve(entry.value);
        }
        if (this.waiter || this.route)
            return Promise.reject(Error('Service channel already has a reader'));
        if (this.socket.readyState !== WebSocket.OPEN) return Promise.reject(closed());
        return new Promise((resolve, reject) => (this.waiter = { resolve, reject }));
    }
    // Publish the connection handler and drain pending input
    activate(route: (value: unknown) => void) {
        if (this.waiter) throw Error('Authentication left a pending reader');
        this.route = route;
        for (const entry of this.queue.splice(0)) route(entry.value);
        this.bytes = 0;
    }
    // Retire connection work and release pending requests
    close() {
        this.queue = [];
        this.bytes = 0;
        this.waiter?.reject(closed());
        this.waiter = undefined;
        this.socket.terminate();
    }
}
export class Session {
    private pending = new Map<string, (error?: Error, value?: unknown) => void>();
    private stopped = false;
    private heartbeat: ReturnType<typeof setInterval>;
    private pong = performance.now();
    // Bind the connection and start its heartbeat lifecycle
    constructor(
        private channel: Channel,
        route: Route,
        private onClosed: () => void = () => {},
    ) {
        channel.socket.on('pong', () => (this.pong = performance.now()));
        channel.socket.once('close', () => this.close());
        this.heartbeat = setInterval(
            // Detect a missed heartbeat before sending the next liveness probe
            () => {
                if (performance.now() - this.pong >= 90000) {
                    this.close();
                    return;
                }
                channel.socket.ping(undefined, undefined, (error) => {
                    if (error) this.close();
                });
            },
            30000,
        );
        this.heartbeat.unref();
        try {
            channel.activate((value) => {
                const id = route(value);
                if (id) this.pending.get(id)?.(undefined, value);
            });
        } catch (error) {
            this.close();
            throw error;
        }
    }
    // Report whether this connection can still accept commands
    get alive() {
        return !this.stopped && this.channel.socket.readyState === WebSocket.OPEN;
    }
    // Issue a correlated command with cancellation and a deadline
    request(id: string, value: unknown, signal?: AbortSignal): Promise<unknown> {
        if (signal?.aborted)
            return Promise.reject(signal.reason ?? Error('Service request cancelled'));
        if (!id || Buffer.byteLength(id) > 128)
            return Promise.reject(Error('Invalid service request ID'));
        let text: string;
        try {
            text = encode(value);
        } catch (error) {
            return Promise.reject(error);
        }
        if (!this.alive) return Promise.reject(closed());
        if (this.pending.size >= 32 || this.pending.has(id))
            return Promise.reject(Error('Duplicate request ID or service request queue full'));
        return new Promise((resolve, reject) => {
            let sent = false;
            // Cancel this command without retaining its listeners
            const abort = () => {
                finish(Error('Service request cancelled'));
                if (!sent) this.close();
            };
            const timeout = setTimeout(() => {
                finish(Error('Service request timed out'));
                if (!sent) this.close();
            }, 8000);
            // Settle the command once and remove its pending state
            const finish = (error?: Error, result?: unknown) => {
                if (this.pending.get(id) !== finish) return;
                this.pending.delete(id);
                clearTimeout(timeout);
                signal?.removeEventListener('abort', abort);
                if (error) reject(error);
                else resolve(result);
            };
            this.pending.set(id, finish);
            signal?.addEventListener('abort', abort, { once: true });
            void this.channel.sendEncoded(text).then(
                () => {
                    sent = true;
                },
                (error) => {
                    finish(error);
                    this.close();
                },
            );
        });
    }
    // Retire connection work and release pending requests
    close() {
        if (this.stopped) return;
        this.stopped = true;
        clearInterval(this.heartbeat);
        this.channel.close();
        for (const finish of [...this.pending.values()]) finish(closed());
        try {
            this.onClosed();
        } catch {
            /* resource cleanup must finish */
        }
    }
}
export class ServiceClient {
    private session?: Session;
    private key = '';
    private opening = false;
    private generation = 0;
    private cancel?: () => void;
    // Retain the endpoint and authentication options used for reconnects
    constructor(readonly network = new NetworkPayload()) {}
    // Retire connection work and release pending requests
    close() {
        this.generation++;
        this.cancel?.();
        this.cancel = undefined;
        const session = this.session;
        this.session = undefined;
        this.key = '';
        session?.close();
    }
    // Open a service connection using the configured handshake
    async connect(
        key: string,
        endpoint: string,
        handshake: (channel: Channel) => Promise<void>,
        route: Route,
        signal?: AbortSignal,
        onClosed?: () => void,
    ): Promise<{ session: Session; created: boolean }> {
        if (signal?.aborted) throw signal.reason ?? closed();
        if (this.opening) throw Error('WebSocket connection already opening');
        if (this.key === key && this.session?.alive)
            return { session: this.session, created: false };
        let url: URL;
        try {
            url = new URL(endpoint);
        } catch {
            throw Error('Invalid WebSocket URL');
        }
        if (
            !['ws:', 'wss:'].includes(url.protocol) ||
            !url.hostname ||
            url.username ||
            url.password ||
            url.search ||
            url.hash
        )
            throw Error('Use ws:// or wss:// without credentials or query parameters');
        this.close();
        const generation = this.generation;
        const socket = new WebSocket(url, {
            maxPayload: MAX_FRAME,
            perMessageDeflate: false,
            followRedirects: false,
            handshakeTimeout: 10000,
            // Create the underlying authenticated WebSocket connection
            createConnection: (options: any) => {
                const stream =
                    url.protocol === 'wss:'
                        ? tls({ ...options, rejectUnauthorized: true, minVersion: 'TLSv1.2' })
                        : tcp(options);
                const write = stream.write.bind(stream);
                (stream as any).write = (chunk: any, ...args: any[]) => {
                    const size =
                        typeof chunk === 'string' ? Buffer.byteLength(chunk) : chunk.byteLength;
                    this.network.add(size, 0);
                    this.network.diagnostics.io('write', size);
                    return (write as any)(chunk, ...args);
                };
                stream.on('data', (chunk) => {
                    this.network.add(0, chunk.length);
                    this.network.diagnostics.io('read', chunk.length);
                });
                return stream;
            },
        });
        const channel = new Channel(socket);
        this.opening = true;
        // Reject connection setup when its owner cancels
        let rejectAbort: (error: Error) => void = () => {};
        const abort = new Promise<never>((_, reject) => (rejectAbort = reject));
        // Cancel the pending connection handshake
        const cancel = () => {
            channel.close();
            rejectAbort(closed());
        };
        this.cancel = cancel;
        const timeout = setTimeout(cancel, 10000);
        signal?.addEventListener('abort', cancel, { once: true });
        try {
            await Promise.race([
                (async () => {
                    await new Promise<void>((resolve, reject) => {
                        socket.once('open', resolve);
                        socket.once('close', () => reject(closed()));
                    });
                    await handshake(channel);
                })(),
                abort,
            ]);
            if (generation !== this.generation || signal?.aborted) throw closed();
            const session = new Session(channel, route, onClosed);
            if (!session.alive) throw closed();
            this.session = session;
            this.key = key;
            return { session, created: true };
        } catch (error) {
            channel.close();
            throw error;
        } finally {
            clearTimeout(timeout);
            signal?.removeEventListener('abort', cancel);
            if (this.cancel === cancel) this.cancel = undefined;
            this.opening = false;
        }
    }
}
