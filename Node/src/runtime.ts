import { finiteJSONValue } from './internal/json.js';
import process from 'node:process';
import { launchOwnerExited } from './internal/owner-lifecycle.js';
import { byteLength } from './internal/bytes.js';
import {
    FEATURES,
    HostInfo,
    MAX_MESSAGE,
    UnsupportedFeatureError,
    parseEvent,
} from './protocol.js';
import type { Event, Settings } from './protocol.js';
import { NetworkPayload } from './network.js';
import { IncomingBudget } from './internal/incoming.js';
import { control, type Control } from './control.js';
import { THEME_EVENT, THEME_PROTOCOL, decodePluginTheme, type PluginTheme } from './theme.js';
export interface Handler {
    // Dispatch a host event to the plugin handler
    handle(client: Client, event: Event): void | Promise<void>;
    // Run due plugin work using the supplied client
    poll?(client: Client): void | Promise<void>;
    // Report the next requested plugin deadline
    nextPoll?(now: number): number | undefined;
    // Coalesce dial events when the handler supports it
    batchDial?(action: string): boolean;
    // Decide whether an action should produce an automatic log
    shouldLogAction?(event: Event): boolean;
    // Retire owned plugin work and prevent further admission
    close?(): void | Promise<void>;
}
// Decide whether an action should produce an automatic log
export function shouldLogAction(event: Event): boolean {
    if (event.event === 'keyDown' || event.event === 'dialDown') return true;
    if (event.event === 'dialRotate') return (event.payload?.ticks ?? 0) !== 0;
    if (event.event !== 'touchTap') return false;
    const touch = event.streamDockManager?.touch;
    return touch ? ['tap', 'end', ''].includes(touch.phase ?? '') : !event.payload?.hold;
}
export interface Deferred {
    // Settle owned work once and release its pending resources
    finish(error?: unknown): void;
}
interface Receipt {
    pending: number;
    failed: boolean;
    sent: boolean;
    // Settle owned work once and release its pending resources
    finish(): void;
}
export class ProbePool {
    #queue: { work: () => Promise<void>; signal: AbortSignal }[] = [];
    #active = 0;
    #closed = false;
    // Admit background work within the queue budget
    submit(signal: AbortSignal, work: () => Promise<void>): boolean {
        if (this.#closed || signal.aborted || this.#queue.length >= 32) return false;
        this.#queue.push({ signal, work });
        this.#pump();
        return true;
    }
    // Start queued work while execution capacity is available
    #pump(): void {
        while (!this.#closed && this.#active < 4 && this.#queue.length) {
            const job = this.#queue.shift()!;
            if (job.signal.aborted) continue;
            this.#active++;
            Promise.resolve()
                .then(job.work)
                .finally(() => {
                    this.#active--;
                    this.#pump();
                });
        }
    }
    // Retire owned plugin work and prevent further admission
    close(): void {
        this.#closed = true;
        this.#queue = [];
    }
}
export const MAX_OUTPUT_BYTES = 8 * MAX_MESSAGE;
export const MAX_LOG_BYTES = MAX_MESSAGE;
function networkDemand(client: Client): boolean {
    const run = client.host.raw.streamDockManager?.run_id;
    return (
        client.supports('plugin-protocol-v1') &&
        client.supports('network-demand-v1') &&
        client.supports('network-payload-v1') &&
        typeof run === 'string' &&
        run.length > 0 &&
        byteLength(run) <= 128
    );
}
const drainWire = Symbol('drainWire');
interface Queued {
    encoded: string;
    bytes: number;
}
export class Client {
    control?: Control;
    #pluginTheme?: PluginTheme;
    readonly network = new NetworkPayload();
    readonly stop = new AbortController();
    #output: Queued[] = [];
    #outputBytes = 0;
    #logs: Queued[] = [];
    #logBytes = 0;
    #droppedLogs = 0;
    outputReady: () => void = () => {};
    #receipt?: Receipt;
    #closing = false;
    #inputs = new Map<string, { context: string; receipt: Deferred }>();
    #actions = new Map<string, { context: string; receipt: Receipt }>();
    #jobs = new Set<AbortController>();
    #owners = new Map<AbortController, string>();
    #pool = new ProbePool();
    #applies: (() => Promise<void>)[] = [];
    wake: () => void = () => {};
    // Initialize host output, deferred work and negotiated feature state
    constructor(
        readonly host = new HostInfo(),
        readonly identity = '',
    ) {
        this.setNetworkDebug(host.raw.streamDockManager?.debug_network === true);
    }
    // Check a negotiated host capability
    supports(feature: string): boolean {
        return this.host.supports(feature);
    }
    // Check whether the host supports theme RPC
    supportsThemeRPC(): boolean {
        return this.supports(THEME_PROTOCOL) && this.host.raw.streamDockManager?.theme_rpc === true;
    }
    // Request the current plugin theme from the host
    async getPluginTheme(signal?: AbortSignal): Promise<PluginTheme> {
        if (!this.supportsThemeRPC()) throw new UnsupportedFeatureError(THEME_PROTOCOL);
        const theme = decodePluginTheme(
            await control(this).request(THEME_PROTOCOL, 'get', {}, signal),
        );
        this.#rememberPluginTheme(theme);
        return this.currentPluginTheme()!;
    }
    // Return the last known plugin theme
    currentPluginTheme(): PluginTheme | undefined {
        return this.#pluginTheme && structuredClone(this.#pluginTheme);
    }
    // Update the cached host theme
    #rememberPluginTheme(theme: PluginTheme): boolean {
        if (this.#pluginTheme && this.#pluginTheme.revision >= theme.revision) return false;
        this.#pluginTheme = structuredClone(theme);
        return true;
    }
    // Queue a context-scoped host event
    send(
        event: string,
        context = '',
        payload?: Settings,
        action?: string,
        control?: Pick<Event, 'protocol_id' | 'request_id' | 'method'>,
    ): void {
        if (this.stop.signal.aborted) throw Error('SDK session closed');
        if (event === 'streamDockManagerRpcRequest' && !this.supports(control?.protocol_id ?? ''))
            throw new UnsupportedFeatureError(control?.protocol_id ?? '');
        if (FEATURES[event] && !this.supports(FEATURES[event]))
            throw new UnsupportedFeatureError(FEATURES[event]);
        const message: Event = {
            event,
            ...(context ? { context } : {}),
            ...(payload !== undefined ? { payload } : {}),
            ...(action ? { action } : {}),
            ...(control?.protocol_id
                ? {
                      protocol_id: control.protocol_id,
                      request_id: control.request_id,
                      method: control.method,
                  }
                : {}),
        };
        const encoded = JSON.stringify(message, finiteJSONValue());
        const bytes = byteLength(encoded);
        if (bytes > MAX_MESSAGE) throw Error('SDK message exceeds 1048576-byte UTF-8 JSON limit');
        const output = event === 'logMessage' ? this.#logs : this.#output;
        if (
            output.length >= 128 ||
            (event === 'logMessage'
                ? this.#logBytes + bytes > MAX_LOG_BYTES
                : this.#outputBytes + bytes > MAX_OUTPUT_BYTES)
        ) {
            if (event === 'logMessage') this.#droppedLogs++;
            throw Error('SDK send queue full');
        }
        if (event === 'desktopInput' && this.#receipt) {
            const id = payload?.request_id;
            if (typeof id !== 'string' || !id || this.#inputs.has(id) || this.#inputs.size >= 128)
                throw Error('Invalid desktop request');
            this.#inputs.set(id, { context, receipt: this.deferAction() });
        }
        output.push({ encoded, bytes });
        if (event === 'logMessage') this.#logBytes += bytes;
        else this.#outputBytes += bytes;
        this.outputReady();
    }
    // Drain queued output within the supplied byte budget
    drain(maxBytes = Infinity): Event[] {
        return this[drainWire](maxBytes).map((frame) => JSON.parse(frame));
    }
    [drainWire](maxBytes: number): string[] {
        const values: string[] = [];
        // Take the next queued output frame
        const take = (queue: Queued[], logs: boolean) => {
            while (queue.length && queue[0].bytes <= maxBytes) {
                const value = queue.shift()!;
                maxBytes -= value.bytes;
                if (logs) this.#logBytes -= value.bytes;
                else this.#outputBytes -= value.bytes;
                values.push(value.encoded);
            }
        };
        take(this.#output, false);
        if (this.#output.length) return values;
        if (this.#droppedLogs) {
            const message: Event = {
                event: 'logMessage',
                payload: {
                    message: `SDK log queue overloaded; ${this.#droppedLogs} messages were not captured`,
                    incomplete: true,
                    ...(this.supports('plugin-logs-v1') ? { stream: 'stderr' } : {}),
                },
            };
            const encoded = JSON.stringify(message);
            const bytes = byteLength(encoded);
            if (bytes > maxBytes) return values;
            maxBytes -= bytes;
            values.push(encoded);
            this.#droppedLogs = 0;
        }
        take(this.#logs, true);
        return values;
    }
    // Report whether encoded host output remains queued
    get hasOutput(): boolean {
        return !!(this.#output.length || this.#logs.length || this.#droppedLogs);
    }
    // Publish a control title
    title(id: string, title: string): void {
        this.send('setTitle', id, { title, target: 0 });
    }
    // Publish a control image using the negotiated host format
    image(id: string, image: string): void {
        this.send('setImage', id, { image, target: 0 });
    }
    // Select the visible action state
    state(id: string, state: number): void {
        this.send('setState', id, { state });
    }
    // Save action settings through the host
    settings(id: string, value: Settings): void {
        this.send('setSettings', id, value);
    }
    // Request the current action settings
    getSettings(id: string): void {
        this.send('getSettings', id);
    }
    /**
     * Send VSD Craft's backend settings request using the registration UUID.
     * VSD Craft documents an asynchronous didReceiveSettings event.
     */
    requestVsdCraftSettings(): void {
        if (!this.identity.trim()) throw Error('VSD Craft settings require a registration UUID');
        this.send('getSettings', this.identity);
    }
    // Save plugin-wide settings through the host
    globalSettings(value: Settings): void {
        this.send('setGlobalSettings', this.identity, value);
    }
    // Request plugin-wide settings
    getGlobalSettings(): void {
        this.send('getGlobalSettings', this.identity);
    }
    // Publish structured control feedback
    feedback(id: string, value: Settings): void {
        this.send('setFeedback', id, value);
    }
    // Select a control feedback layout
    feedbackLayout(id: string, layout: string): void {
        this.send('setFeedbackLayout', id, { layout });
    }
    // Send a message to the action inspector
    toInspector(id: string, value: Settings, action: string): void {
        if (!action) throw Error('Inspector messaging requires an action UUID');
        this.send('sendToPropertyInspector', id, value, action);
    }
    // Request a temporary alert indicator
    alert(id: string): void {
        this.send('showAlert', id, {});
    }
    // Request a temporary success indicator
    ok(id: string): void {
        this.send('showOk', id, {});
    }
    #jobRedactors = new Map<AbortController, (text: string) => string>();
    #safeLog(text: string): string {
        const current = this.logRedactor;
        for (const sanitize of new Set(this.#jobRedactors.values()))
            if (sanitize !== current) text = sanitize(text);
        return current(text);
    }
    logRedactor: (message: string) => string = (message) => message;
    // Emit a redacted plugin log message
    log(message: string): void {
        this.logStream('stdout', message);
    }
    // Create a context-aware log stream
    logStream(stream: 'stdout' | 'stderr', message: string): void {
        if (stream !== 'stdout' && stream !== 'stderr') throw Error('Invalid log stream');
        const raw = new TextEncoder().encode(this.#safeLog(message));
        const typed = this.host.supports('plugin-logs-v1');
        const decoder = new TextDecoder();
        for (let start = 0; start < raw.length;) {
            let end = Math.min(raw.length, start + (16 << 10));
            while (end < raw.length && (raw[end] & 0xc0) === 0x80) end--;
            const frame = decoder.decode(raw.subarray(start, end));
            this.send('logMessage', '', typed ? { message: frame, stream } : { message: frame });
            start = end;
        }
    }
    // Record the result of an automatically logged action
    #automaticLog(stream: 'stdout' | 'stderr', message: string): void {
        try {
            this.logStream(stream, message);
        } catch {
            /* Logging never fails an action */
        }
    }
    // Apply the host network-diagnostic policy
    setNetworkDebug(enabled: boolean): void {
        this.network.diagnostics.configure(
            enabled && this.supports('plugin-logs-v1'),
            (payload, failed) => {
                try {
                    this.send('logMessage', '', {
                        ...payload,
                        stream: failed ? 'stderr' : 'stdout',
                        source: 'network',
                    });
                } catch {}
            },
            (text) => this.#safeLog(text),
        );
    }
    // Expose the safe stdout log writer
    stdout(): { write: (message: string) => void } {
        return {
            // Write through the redacted diagnostic stream
            write: (message) => this.logStream('stdout', message),
        };
    }
    // Expose the safe stderr log writer
    stderr(): { write: (message: string) => void } {
        return {
            // Write through the redacted diagnostic stream
            write: (message) => this.logStream('stderr', message),
        };
    }
    // Ask the host to open a supported URL
    openUrl(url: string): void {
        this.send('openUrl', '', { url });
    }
    // Subscribe to the negotiated telemetry stream
    subscribeTelemetry(topics: string[]): void {
        this.send('subscribeTelemetry', '', { topics });
    }
    // Send inspector discovery choices
    dynamicItems(value: Settings): void {
        this.send('setDynamicItems', '', value);
    }
    // Request a negotiated desktop-input operation
    desktopInput(id: string, value: Settings): void {
        this.send('desktopInput', id, value);
    }
    // Publish the requested semantic display
    display(
        id: string,
        value: {
            title: string;
            image?: string;
            state?: number;
            layout?: string;
            feedback?: Settings;
        },
    ): void {
        if (value.layout) this.feedbackLayout(id, value.layout);
        this.state(id, value.state ?? 0);
        if (value.image) this.image(id, value.image);
        this.title(id, value.title);
        if (value.feedback) this.feedback(id, value.feedback);
    }
    // Track an action whose completion will arrive later
    deferAction(): Deferred {
        const receipt = this.#receipt;
        if (receipt) receipt.pending++;
        let done = false;
        return {
            // Settle owned work once and release its pending resources
            finish: (error?: unknown) => {
                if (done) return;
                done = true;
                if (receipt && !receipt.sent) {
                    receipt.failed ||= error != null;
                    receipt.pending--;
                    receipt.finish();
                }
            },
        };
    }
    // Schedule work owned by the current plugin session
    background<T>(
        context: string,
        work: (signal: AbortSignal) => Promise<T>,
        apply: (value: T) => void | Promise<void>,
        trackAction = true,
    ): AbortController {
        if (this.#jobs.size >= 36) throw Error('plugin job queue full');
        const cancel = new AbortController(),
            receipt = trackAction
                ? this.deferAction()
                : {
                      // Settle owned work once and release its pending resources
                      finish: (_error?: unknown) => {},
                  };
        this.#jobs.add(cancel);
        cancel.signal.addEventListener(
            'abort',
            () => {
                receipt.finish(Error('background cancelled'));
                this.#jobs.delete(cancel);
                this.#owners.delete(cancel);
            },
            { once: true },
        );
        const sanitize = this.logRedactor;
        const accepted = this.#pool.submit(cancel.signal, async () => {
            this.#jobRedactors.set(cancel, sanitize);
            await Promise.resolve()
                .then(() => work(cancel.signal))
                .then(
                    // Queue completion only while this client still owns the background work
                    (value) => {
                        if (cancel.signal.aborted) return;
                        this.#applies.push(async () => {
                            this.#jobRedactors.set(cancel, sanitize);
                            try {
                                if (!cancel.signal.aborted) await apply(value);
                                receipt.finish();
                            } catch (error) {
                                receipt.finish(error);
                            } finally {
                                this.#jobRedactors.delete(cancel);
                                this.#jobs.delete(cancel);
                                this.#owners.delete(cancel);
                            }
                        });
                        this.wake();
                    },
                    (error) => {
                        this.#jobs.delete(cancel);
                        this.#owners.delete(cancel);
                        receipt.finish(error);
                        this.wake();
                    },
                )
                .finally(() => this.#jobRedactors.delete(cancel));
        });
        if (!accepted) {
            cancel.abort();
            throw Error('SDK background queue full');
        }
        this.#owners.set(cancel, context);
        return cancel;
    }
    // Apply completed background jobs and invoke handler polling when due
    async poll(handler: Handler, pollDue = true): Promise<void> {
        for (const apply of this.#applies.splice(0)) await apply();
        if (pollDue) await handler.poll?.(this);
    }
    // Deliver one event while preserving handler ordering
    async dispatch(handler: Handler, event: Event): Promise<void> {
        if (event.event === THEME_EVENT) {
            if (!this.supportsThemeRPC()) return;
            try {
                if (!this.#rememberPluginTheme(decodePluginTheme(event.payload))) return;
            } catch {
                return;
            }
        }
        if (event.event === 'requestNetworkPayload' && networkDemand(this)) {
            const run = this.host.raw.streamDockManager?.run_id;
            if (typeof run === 'string' && run.length > 0 && run.length <= 128) {
                const { sent, received } = this.network.snapshot();
                this.send('networkPayload', '', {
                    run_id: run,
                    sent_bytes: sent,
                    received_bytes: received,
                });
            }
            return;
        }
        if (event.event === 'didReceiveLogSettings' && this.supports('plugin-logs-v1')) {
            if (typeof event.payload?.debug_network === 'boolean')
                this.setNetworkDebug(event.payload.debug_network);
            return;
        }
        const logAction =
            this.supports('plugin-logs-v1') &&
            (handler.shouldLogAction?.(event) ?? shouldLogAction(event));
        const started = performance.now();
        if (event.event === 'willDisappear') {
            for (const [id, input] of this.#inputs)
                if (input.context === event.context) {
                    input.receipt.finish(Error('Context removed'));
                    this.#inputs.delete(id);
                }
            for (const [id, action] of this.#actions)
                if (action.context === event.context) {
                    action.receipt.failed = true;
                    action.receipt.pending = 0;
                    action.receipt.finish();
                    this.#actions.delete(id);
                }
        }
        if (event.event === 'willDisappear')
            for (const [cancel, context] of this.#owners)
                if (context === event.context) {
                    cancel.abort();
                    this.#owners.delete(cancel);
                }
        if (event.event === 'desktopInputResult') {
            const id = event.payload?.request_id;
            const pending = this.#inputs.get(id);
            if (pending && pending.context === event.context) {
                pending.receipt.finish(
                    event.payload?.error ? Error('desktop input failed') : undefined,
                );
                this.#inputs.delete(id);
            }
        }
        const id = event.streamDockManager?.request_id;
        const old = this.#receipt;
        const receipt: Receipt = {
            pending: 1,
            failed: false,
            sent: false,
            // Settle owned work once and release its pending resources
            finish: () => {
                if (receipt.pending === 0 && !receipt.sent) {
                    receipt.sent = true;
                    if (logAction)
                        this.#automaticLog(
                            receipt.failed ? 'stderr' : 'stdout',
                            `Action ${event.action ?? ''} ${receipt.failed ? 'failed or cancelled' : 'completed'} in ${Math.floor(performance.now() - started)} ms`,
                        );
                    if (id) this.#actions.delete(id);
                    if (id && !this.#closing && this.supports('action-results-v1'))
                        this.send('actionResult', event.context, {
                            request_id: id,
                            success: !receipt.failed,
                        });
                }
            },
        };
        if (id) this.#actions.set(id, { context: event.context ?? '', receipt });
        this.#receipt = receipt;
        try {
            await handler.handle(this, event);
            if (
                this.supports('plugin-logs-v1') &&
                ['didReceiveSettings', 'didReceiveGlobalSettings'].includes(event.event)
            )
                this.#automaticLog('stdout', 'Settings updated');
        } catch (error) {
            receipt.failed = true;
            this.#automaticLog(
                'stderr',
                `Action failure: ${error instanceof Error ? error.message : String(error)}`,
            );
            try {
                this.title(
                    event.context ?? '',
                    `Error: ${error instanceof Error ? error.message : String(error)}`,
                );
                this.send('showAlert', event.context, {});
            } catch {
                /* Preserve the original failed receipt if feedback is full */
            }
        } finally {
            this.#receipt = old;
            receipt.pending--;
            receipt.finish();
        }
    }
    // Retire owned plugin work and prevent further admission
    close(): void {
        this.control?.close();
        this.setNetworkDebug(false);
        this.#closing = true;
        for (const input of this.#inputs.values())
            input.receipt.finish(Error('SDK session closed'));
        this.#inputs.clear();
        for (const action of this.#actions.values()) {
            action.receipt.failed = true;
            action.receipt.pending = 0;
            action.receipt.finish();
        }
        this.#actions.clear();
        for (const job of this.#jobs) job.abort();
        this.#owners.clear();
        this.#pool.close();
        this.stop.abort();
    }
}
export class DialBatch {
    pending?: Event;
    deadline?: number;
    // Flush admitted output to the active transport
    flush(): Event[] {
        const event = this.pending;
        this.pending = undefined;
        this.deadline = undefined;
        return event ? [event] : [];
    }
    // Validate and admit an incoming host frame
    accept(event: Event, eligible: boolean, now: number): Event[] {
        if (!eligible || event.event !== 'dialRotate') return [...this.flush(), event];
        const ticks = event.payload?.ticks ?? 0,
            old = this.pending?.payload?.ticks ?? 0,
            sum = old + ticks;
        if (
            this.pending &&
            now < this.deadline! &&
            event.context === this.pending.context &&
            event.action === this.pending.action &&
            event.device === this.pending.device &&
            old > 0 === ticks > 0 &&
            Number.isSafeInteger(sum)
        ) {
            this.pending.payload!.ticks = sum;
            return [];
        }
        const out = this.flush();
        this.pending = structuredClone(event);
        this.pending.payload ??= {};
        this.pending.payload.ticks = ticks;
        this.deadline = now + 16;
        return out;
    }
}
declare const __STREAM_DOCK_MANAGER_CAPABILITIES__: Settings | null;
// Read capabilities embedded in the compiled plugin
function compiledCapabilities(): Settings | undefined {
    return typeof __STREAM_DOCK_MANAGER_CAPABILITIES__ === 'undefined'
        ? undefined
        : (__STREAM_DOCK_MANAGER_CAPABILITIES__ ?? undefined);
}
export interface LaunchOptions {
    port: number;
    identity: string;
    host: HostInfo;
    descriptor?: Settings;
}
// Parse host launch arguments and environment metadata
export function parseLaunch(args: string[]): LaunchOptions {
    if (args.length % 2) throw Error('Invalid authenticated host arguments');
    const entries = Array.from({ length: args.length / 2 }, (_, i) => [
        args[i * 2].replace(/^--/, '-'),
        args[i * 2 + 1],
    ]);
    if (
        entries.some(
            ([key]) => !['-port', '-pluginUUID', '-registerEvent', '-info'].includes(key),
        ) ||
        new Set(entries.map(([key]) => key)).size !== entries.length
    )
        throw Error('Invalid authenticated host arguments');
    const options = Object.fromEntries(entries);
    const port = Number(options['-port']);
    if (
        !Number.isInteger(port) ||
        port < 1 ||
        port > 65535 ||
        !options['-pluginUUID'] ||
        options['-registerEvent'] !== 'registerPlugin'
    )
        throw Error('Invalid authenticated host arguments');
    return {
        port,
        identity: options['-pluginUUID'],
        host: new HostInfo(JSON.parse(options['-info'] || '{}')),
    };
}
/** Run a Node.js plugin with the host's command-line launch arguments. */
export function run(
    handler: Handler,
    args: string[] = process.argv.slice(2),
    descriptor?: Settings,
): Promise<void> {
    return connect(handler, { ...parseLaunch(args), descriptor });
}

// Connect the plugin to the host transport
export async function connect(handler: Handler, options: LaunchOptions): Promise<void> {
    const descriptor = options.descriptor ?? compiledCapabilities();
    for (const capability of descriptor?.required_capabilities ?? [])
        if (!options.host.supports(capability)) throw new UnsupportedFeatureError(capability);
    return new TransportSession(handler, options, descriptor).start();
}

/** One connection owns all admitted leases until their callback or retirement completes.
 * Graceful shutdown queues socket close behind admitted work. Fatal stop retires only
 * queued work; final close waits for the running callback before closing the handler.
 */
class TransportSession {
    readonly client: Client;
    readonly socket: WebSocket;
    readonly batch = new DialBatch();
    readonly incoming = new IncomingBudget();
    readonly retained = new Map<Event, () => void>();
    readonly queued: { work: () => Promise<void>; release: () => void }[] = [];
    private readonly launchOwner = process.ppid;
    private opened = false;
    private ownerEnded = false;
    private clientClosed = false;
    private socketClosing = false;
    private listeners: (() => void)[] = [];
    // Own one transport connection and its ordered event lifecycle
    constructor(
        private readonly handler: Handler,
        private readonly options: LaunchOptions,
        private readonly descriptor: Settings | undefined,
    ) {
        this.client = new Client(options.host, options.identity);
        this.socket = new WebSocket(`ws://127.0.0.1:${options.port}`);
    }
    // Register an owned transport listener
    private listen<K extends keyof WebSocketEventMap>(
        type: K,
        listener: (event: WebSocketEventMap[K]) => void,
    ) {
        this.socket.addEventListener(type, listener);
        this.listeners.push(() => this.socket.removeEventListener(type, listener));
    }
    // Remove transport listeners during retirement
    private removeListeners() {
        for (const remove of this.listeners.splice(0)) remove();
    }
    // Return the byte reservation held by an event
    private releaseEvent(event: Event) {
        this.retained.get(event)?.();
        this.retained.delete(event);
    }
    // Release queued events that can no longer be delivered
    private retireQueued() {
        const jobs = this.queued.splice(0);
        for (const job of jobs) job.release();
        for (const event of this.batch.flush()) this.releaseEvent(event);
        return jobs.length;
    }
    // Retire the client and its admitted work
    private closeClient() {
        if (this.clientClosed) return;
        this.clientClosed = true;
        this.client.close();
    }
    // Close the owned host WebSocket
    private closeSocket() {
        if (this.socketClosing) return;
        this.socketClosing = true;
        this.socket.close();
    }
    private readonly state = {
        networkTimer: undefined as ReturnType<typeof setInterval> | undefined,
        timer: undefined as ReturnType<typeof setTimeout> | undefined,
        outputTimer: undefined as ReturnType<typeof setTimeout> | undefined,
        owner: Promise.resolve(),
        closed: false,
        accepting: true,
        closing: false,
        running: false,
        failure: undefined as unknown,
        pending: 0,
        wakeQueued: false,
        flushQueued: false,
        pollBackoff: 0,
        pollUntil: 0,
    };
    // Start the plugin session lifecycle
    start(): Promise<void> {
        const { client, socket, batch, incoming, retained, queued } = this;
        const { handler, options, descriptor } = this;
        // Return the byte reservation held by an event
        const releaseEvent = (event: Event) => this.releaseEvent(event);
        // Release queued events that can no longer be delivered
        const retireQueued = () => {
            this.state.pending -= this.retireQueued();
        };
        // Prevent new events from entering this session
        const stopAdmission = () => {
            this.state.accepting = false;
            this.state.closing = true;
            retireQueued();
            this.closeClient();
        };
        // Retire the session after a transport or handler failure
        const fail = (error: unknown) => {
            this.state.failure ??= error;
            stopAdmission();
            this.closeSocket();
        };
        // Schedule a bounded output flush
        const queueFlush = () => {
            if (this.state.flushQueued || this.state.closed) return;
            this.state.flushQueued = true;
            queueMicrotask(() => {
                this.state.flushQueued = false;
                flush();
            });
        };
        client.outputReady = queueFlush;
        // Flush admitted output to the active transport
        const flush = () => {
            if (socket.readyState !== WebSocket.OPEN) return;
            for (const frame of client[drainWire](
                Math.max(0, MAX_OUTPUT_BYTES - socket.bufferedAmount),
            ))
                socket.send(frame);
            if (client.hasOutput)
                this.state.outputTimer ??= setTimeout(() => {
                    this.state.outputTimer = undefined;
                    queueFlush();
                }, 16);
        };
        // Run the session poll and back off after handler failures
        const poll = async () => {
            const due = Date.now() >= this.state.pollUntil;
            try {
                await client.poll(handler, due);
                if (due) {
                    this.state.pollBackoff = 0;
                    this.state.pollUntil = 0;
                }
            } catch (error) {
                this.state.pollBackoff = Math.min(
                    60000,
                    this.state.pollBackoff ? this.state.pollBackoff * 2 : 1000,
                );
                this.state.pollUntil = Date.now() + this.state.pollBackoff;
                try {
                    client.logStream(
                        'stderr',
                        String(error instanceof Error ? error.message : error),
                    );
                } catch {}
            }
        };
        // Schedule the next session polling turn
        const schedule = () => {
            clearTimeout(this.state.timer);
            // Poll callbacks alone do not opt into periodic work
            let next = handler.nextPoll?.(Date.now());
            if (next !== undefined) next = Math.max(next, this.state.pollUntil);
            const deadline =
                next === undefined
                    ? batch.deadline
                    : batch.deadline === undefined
                      ? next
                      : Math.min(next, batch.deadline);
            if (deadline !== undefined)
                this.state.timer = setTimeout(
                    () =>
                        enqueue(
                            // Run due handler polling inside the session ownership queue
                            async () => {
                                if (batch.deadline !== undefined && Date.now() >= batch.deadline)
                                    for (const event of batch.flush()) {
                                        try {
                                            await client.dispatch(handler, event);
                                        } finally {
                                            releaseEvent(event);
                                        }
                                    }
                                await poll();
                            },
                        ),
                    Math.max(0, deadline - Date.now()),
                );
        };
        // Queue an admitted host event
        const enqueue = (work: () => Promise<void>, release = () => {}) => {
            if (this.state.closed || this.state.closing) {
                release();
                return;
            }
            if (this.state.pending >= 128) {
                release();
                fail(Error('SDK incoming queue full'));
                return;
            }
            this.state.pending++;
            queued.push({ work, release });
            if (this.state.running) return;
            this.state.running = true;
            this.state.owner = Promise.resolve().then(
                // Drain admitted events serially and schedule their resulting output
                async () => {
                    while (queued.length && !this.state.closed && !this.state.closing) {
                        const job = queued.shift()!;
                        try {
                            await job.work();
                            if (!this.state.closed && !this.state.closing) {
                                flush();
                                schedule();
                            }
                        } catch (error) {
                            fail(error);
                        } finally {
                            job.release();
                            this.state.pending--;
                        }
                    }
                    this.state.running = false;
                },
            );
        };
        // Validate and retain an event before dispatch
        const enqueueEvent = (event: Event) =>
            enqueue(
                () => client.dispatch(handler, event),
                () => releaseEvent(event),
            );
        client.wake = () => {
            if (this.state.wakeQueued || this.state.closed) return;
            this.state.wakeQueued = true;
            queueMicrotask(() =>
                enqueue(async () => {
                    this.state.wakeQueued = false;
                    await poll();
                }),
            );
        };
        return new Promise((resolve, reject) => {
            this.listen(
                'open',
                // Register the plugin and publish startup metadata when the socket opens
                () => {
                    if (this.state.closed || this.state.closing) return;
                    this.opened = true;
                    const runId = options.host.raw.streamDockManager?.run_id;
                    if (
                        typeof runId === 'string' &&
                        runId.length > 0 &&
                        runId.length <= 128 &&
                        !networkDemand(client) &&
                        client.supports('network-payload-v1')
                    )
                        this.state.networkTimer = setInterval(
                            () =>
                                enqueue(async () => {
                                    const sample = client.network.snapshot();
                                    client.send('networkPayload', '', {
                                        run_id: runId,
                                        sent_bytes: sample.sent,
                                        received_bytes: sample.received,
                                    });
                                }),
                            1000,
                        );
                    socket.send(
                        JSON.stringify({
                            event: 'registerPlugin',
                            uuid: options.identity,
                            ...(options.host.supports('plugin-protocol-v1')
                                ? {
                                      payload: {
                                          capabilities: descriptor ?? null,
                                          network_debug: true,
                                          ...(networkDemand(client)
                                              ? { network_demand: true }
                                              : {}),
                                      },
                                  }
                                : {}),
                        }),
                    );
                    if (client.supportsThemeRPC()) {
                        enqueue(async () => {
                            try {
                                await client.getPluginTheme(client.stop.signal);
                            } catch {
                                /* Legacy theme events remain queued */
                            }
                        });
                    } else schedule();
                },
            );
            this.listen(
                'message',
                // Validate and admit host frames without overlapping handler execution
                ({ data }) => {
                    if (!this.state.accepting) return;
                    let release: (() => void) | undefined;
                    let admittedEvent: Event | undefined;
                    try {
                        const text = String(data);
                        const event = parseEvent(text);
                        if (
                            client.control?.observe(event, byteLength(text)) ||
                            event.event === 'streamDockManagerRpcResponse'
                        )
                            return;
                        release = incoming.acquire(byteLength(text));
                        admittedEvent = event;
                        retained.set(event, release);
                        release = undefined;
                        if (event.event === 'shutdown') {
                            this.state.accepting = false;
                            for (const batched of batch.flush()) enqueueEvent(batched);
                            enqueue(
                                async () => {
                                    this.closeSocket();
                                },
                                () => releaseEvent(event),
                            );
                            return;
                        }
                        const prior = batch.pending;
                        const readyEvents = batch.accept(
                            event,
                            !event.streamDockManager?.request_id &&
                                (handler.batchDial?.(event.action ?? '') ?? false),
                            Date.now(),
                        );
                        if (batch.pending && batch.pending !== prior) {
                            retained.set(batch.pending, retained.get(event)!);
                            retained.delete(event);
                        } else if (!readyEvents.includes(event)) {
                            releaseEvent(event);
                        }
                        for (const ready of readyEvents) enqueueEvent(ready);
                        schedule();
                    } catch (error) {
                        release?.();
                        if (admittedEvent) releaseEvent(admittedEvent);
                        fail(error);
                    }
                },
            );
            this.listen(
                'error',
                // Retire the session when the transport reports a failure
                () => {
                    if (this.ownerEnded) return;
                    // OpenDeck can exit without a WebSocket close handshake. Once
                    // the launch owner has exited, an established idle plugin has
                    // no owner to serve. Do not turn that lifecycle end into an
                    // uncaught transport error. Never forgive outstanding work or
                    // infer owner exit merely from close code 1006
                    const ownerEnded =
                        this.opened &&
                        !this.state.closing &&
                        this.state.pending === 0 &&
                        !batch.pending &&
                        !client.hasOutput &&
                        socket.bufferedAmount === 0 &&
                        !client.control?.hasPending &&
                        launchOwnerExited(this.launchOwner);
                    if (ownerEnded) {
                        this.ownerEnded = true;
                        stopAdmission();
                        this.closeSocket();
                    } else {
                        fail(Error('Settings connection unavailable'));
                    }
                },
            );
            this.listen(
                'close',
                // Stop session admission and release queued events after transport closure
                () => {
                    if (this.state.closed) return;
                    this.state.closed = true;
                    stopAdmission();
                    clearTimeout(this.state.timer);
                    clearTimeout(this.state.outputTimer);
                    clearInterval(this.state.networkTimer);
                    this.removeListeners();
                    client.wake = () => {};
                    client.outputReady = () => {};
                    void this.state.owner
                        .then(async () => {
                            await handler.close?.();
                            if (this.state.failure !== undefined) reject(this.state.failure);
                            else resolve();
                        })
                        .catch(reject);
                },
            );
        });
    }
}
