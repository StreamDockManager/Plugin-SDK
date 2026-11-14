import { project } from './settings.js';
import { envelope as discoveryEnvelope } from './discovery.js';
import { updateInterval, NEVER } from './polling.js';
import { Plugin } from './authoring.js';
import type { Display, ViewTarget } from './authoring.js';
import type { Client, Deferred } from './runtime.js';
import type { Event, Settings } from './protocol.js';
import { Resources } from './polling.js';
import { redact } from './credentials.js';
export interface Choice {
    id: string;
    name: string;
    kind?: string;
}
export interface Outcome {
    message?: string;
    value?: unknown;
}
export interface Driver {
    // Derive the shared-read identity for these settings
    key(settings: Settings): string;
    // Fetch the current connector state for a shared key
    read(signal: AbortSignal, settings: Settings): Promise<any>;
    // Execute the requested connector operation
    command(
        signal: AbortSignal,
        action: string,
        settings: Settings,
        ticks: number,
    ): Promise<Outcome>;
    // Resolve dynamic connector choices for the inspector
    discover(signal: AbortSignal, settings: Settings): Promise<Choice[]>;
    // Convert connector state into control feedback
    display(value: any, action: string, settings: Settings): string;
    // Release connector jobs and subscriptions
    close(): void;
    // Record which bindings currently need this connector state
    demand?(settings: Settings[]): void;
    // Publish newly observed connector state to its consumers
    changed?(): boolean;
    // Project the state used for rendering this binding
    visualState?(value: any, action: string): number;
}
export interface Options {
    readTimeout?: number;
    commandTimeout?: number;
    authorizeTimeout?: number;
    actions: string[];
    interval: number;
    sharedKeys?: string[];
    dialActions?: string[];
    targets?: ViewTarget[];
    clock?: () => number;
    lookup?: (key: string) => string | undefined;
}
interface Request {
    requestID?: string;
    scope?: 'action' | 'plugin';
    context: string;
    action: string;
    op: string;
    settings: Settings;
    ticks: number;
    receipt: Deferred;
}
export class Connector extends Plugin {
    private resources = new Resources();
    private queue: Request[] = [];
    private busy = false;
    private messages = new Map<string, string>();
    private activeContext = '';
    private activeDeadline?: number;
    private activeResource?: { key: string; generation: number };
    private activeReceipt?: Deferred;
    private workTail: Promise<unknown> = Promise.resolve();
    // Serialize connector work for a shared resource
    private serial<T>(signal: AbortSignal, work: () => Promise<T>): Promise<T> {
        const result = this.workTail.then(() => {
            signal.throwIfAborted();
            return work();
        });
        this.workTail = result.then(
            () => undefined,
            () => undefined,
        );
        return result;
    }
    // Bind the connector driver, action definitions and scheduling options
    constructor(
        readonly driver: Driver,
        readonly options: Options,
    ) {
        super(
            options.targets ??
                options.actions.map((action) => ({
                    action,
                    updatedBy: options.actions,
                    live: true,
                })),
            options.clock,
        );
        for (const action of options.actions)
            this.register(action, {
                // Dispatch a host event to the connector binding
                handle: (_p, c, e) => this.action(c, e),
                // Refresh connector feedback for the requested context
                render: (_p, id, b) => this.view(id, b.action),
            });
        this.inspector = (_p, c, e) => this.inspect(c, e);
    }
    // Resolve the effective settings of a connector binding
    private bindingSettings(id: string): Settings {
        const s = project(
            this.globals,
            this.bindings.get(id)?.settings ?? {},
            this.options.sharedKeys,
        );
        s._action = this.bindings.get(id)?.action.split('.').at(-1) ?? '';
        return s;
    }
    // Subscribe a binding to its shared connector state
    private watch(): void {
        const demand: Settings[] = [];
        for (const id of this.bindings.keys()) {
            const s = this.bindingSettings(id);
            demand.push(s);
            this.resources.watch(id, this.driver.key(s), updateInterval(s, this.options.interval));
        }
        this.driver.demand?.(demand);
    }
    // Read the latest state visible to this binding
    private view(context: string, action: string): Display {
        this.watch();
        const state = this.resources.snapshot(context),
            value =
                state?.observedAt !== undefined
                    ? this.driver.display(
                          state.lastValue,
                          action.split('.').at(-1) ?? action,
                          this.bindingSettings(context),
                      )
                    : '';
        return {
            title:
                this.messages.get(context) ||
                (state?.stale ? 'Stale\n' : '') + value ||
                'Waiting for connection',
            state:
                state?.observedAt !== undefined
                    ? (this.driver.visualState?.(
                          state.lastValue,
                          action.split('.').at(-1) ?? action,
                      ) ?? 0)
                    : 0,
        };
    }
    // Apply the requested action through the connector
    private action(client: Client, event: Event): void {
        if (!['keyDown', 'dialDown', 'touchTap', 'dialRotate'].includes(event.event)) return;
        if (event.event === 'dialRotate' && !this.batchDial(event.action ?? '')) return;
        this.enqueue(client, {
            context: event.context ?? '',
            action: (event.action ?? '').split('.').at(-1) ?? '',
            op: '',
            settings: this.bindingSettings(event.context ?? ''),
            ticks: event.event === 'dialRotate' ? (event.payload?.ticks ?? 0) : 0,
            receipt: client.deferAction(),
        });
    }
    // Handle inspector discovery and settings requests
    private inspect(client: Client, event: Event): void {
        const p = event.payload ?? {};
        if (!['test', 'discover', 'authorize', 'disconnect'].includes(p.op)) return;
        if (
            p.request_id !== undefined &&
            (typeof p.request_id !== 'string' ||
                new TextEncoder().encode(p.request_id).length > 128)
        )
            throw Error('Invalid discovery request ID');
        const s = project(
            this.globals,
            p.settings ?? {},
            p.scope === 'plugin' ? [] : this.options.sharedKeys,
        );
        this.enqueue(client, {
            context: event.context ?? '',
            action: event.action ?? '',
            op: p.op,
            requestID: p.request_id,
            scope: p.scope,
            settings: s,
            ticks: 0,
            receipt: client.deferAction(),
        });
    }
    // Queue connector work without exceeding its admission policy
    private enqueue(client: Client, request: Request): void {
        if (this.queue.length >= 32) {
            request.receipt.finish(Error('Command queue full'));
            if (request.op)
                client.toInspector(
                    request.context,
                    {
                        error: 'Command queue full; retry after it drains',
                        ...(request.requestID
                            ? { request_id: request.requestID, available: false }
                            : {}),
                    },
                    request.action,
                );
            return;
        }
        this.queue.push(request);
        this.start(client);
    }
    // Start the next admitted connector operation
    private start(client: Client): void {
        if (this.busy) return;
        this.watch();
        const q = this.queue.shift();
        if (q) {
            this.busy = true;
            this.activeContext = q.context;
            this.activeReceipt = q.receipt;
            this.activeResource = undefined;
            this.activeDeadline =
                this.clock() +
                (q.op === 'authorize'
                    ? (this.options.authorizeTimeout ?? 120000)
                    : (this.options.commandTimeout ?? 15000));
            this.background(
                client,
                'connector-work',
                q.context,
                // Execute the queued discovery or command with its operation deadline
                async (signal) => {
                    const timeout =
                        q.op === 'authorize'
                            ? (this.options.authorizeTimeout ?? 120000)
                            : (this.options.commandTimeout ?? 15000);
                    const cancel = AbortSignal.any([signal, AbortSignal.timeout(timeout)]);
                    try {
                        if (q.op === 'discover')
                            return {
                                message: 'Request completed',
                                devices: await this.serial(cancel, () =>
                                    this.driver.discover(cancel, q.settings),
                                ),
                            };
                        if (q.op === 'test') {
                            await this.serial(cancel, () => this.driver.read(cancel, q.settings));
                            return { message: 'Connection succeeded' };
                        }
                        return await this.serial(cancel, () =>
                            this.driver.command(cancel, q.op || q.action, q.settings, q.ticks),
                        );
                    } catch (error) {
                        return { error: redact(error, q.settings, this.options.lookup).message };
                    }
                },
                // Publish the command outcome and settle its deferred action receipt
                (_p, c, value: any) => {
                    this.busy = false;
                    this.activeDeadline = undefined;
                    this.activeReceipt = undefined;
                    this.activeContext = '';
                    this.messages.set(
                        q.context,
                        value.error
                            ? 'Unavailable\n' + value.error
                            : (value.message ?? 'Request completed'),
                    );
                    q.receipt.finish(value.error ? Error(value.error) : undefined);
                    if (q.op)
                        c.toInspector(
                            q.context,
                            q.requestID
                                ? discoveryEnvelope(
                                      {
                                          context: q.context,
                                          action: q.action,
                                          channel: 'service',
                                          id: q.requestID,
                                          operation: q.op,
                                          scope: q.scope,
                                      },
                                      value,
                                      value.error ? 'failed' : '',
                                  ).payload
                                : value,
                            q.action,
                        );
                    for (const state of this.resources.states.values()) state.next = 0;
                    this.start(c);
                },
            );
            return;
        }
        const key = this.resources.due(this.clock())[0];
        if (!key) return;
        const id = [...this.bindings.keys()]
            .sort()
            .sort(
                (a, b) =>
                    (updateInterval(this.bindingSettings(a), this.options.interval) || NEVER) -
                    (updateInterval(this.bindingSettings(b), this.options.interval) || NEVER),
            )
            .find((id) => this.driver.key(this.bindingSettings(id)) === key);
        if (!id) return;
        const settings = this.bindingSettings(id),
            generation = this.resources.begin(key);
        this.busy = true;
        this.activeResource = { key, generation };
        this.activeDeadline = this.clock() + (this.options.readTimeout ?? 12000);
        this.background(
            client,
            'connector-work',
            id,
            async (signal) => {
                try {
                    return {
                        value: await this.serial(signal, () =>
                            this.driver.read(
                                AbortSignal.any([
                                    signal,
                                    AbortSignal.timeout(this.options.readTimeout ?? 12000),
                                ]),
                                settings,
                            ),
                        ),
                    };
                } catch (error) {
                    return {
                        error: redact(error, settings, this.options.lookup).message,
                        retryAt: (error as any)?.retryAt,
                    };
                }
            },
            // Reconcile the shared read result and refresh dependent bindings
            (_p, c, result: any) => {
                this.busy = false;
                this.activeResource = undefined;
                this.activeDeadline = undefined;
                const previous = this.resources.states.get(key);
                const failures = (previous?.failures ?? 0) + 1;
                const recovering =
                    previous?.observedAt === undefined || (previous?.failures ?? 0) > 0;
                if (
                    this.resources.complete(
                        key,
                        generation,
                        result.value,
                        result.error,
                        this.clock(),
                        0.5,
                        result.retryAt,
                    )
                ) {
                    if (c.supports('plugin-logs-v1')) {
                        try {
                            if (result.error && (failures & (failures - 1)) === 0)
                                c.logStream(
                                    'stderr',
                                    `Connection read failed (${failures} consecutive failures): ${result.error}`,
                                );
                            else if (!result.error && recovering)
                                c.logStream('stdout', 'Connection available');
                        } catch {}
                    }
                    for (const context of this.bindings.keys())
                        if (this.driver.key(this.bindingSettings(context)) === key) {
                            this.messages.delete(context);
                            if (
                                result.error &&
                                this.resources.snapshot(context)?.observedAt === undefined
                            )
                                this.messages.set(context, 'Unavailable\n' + result.error);
                        }
                }
                this.start(c);
            },
        );
    }
    // Dispatch a host event to the connector binding
    async handle(client: Client, event: Event): Promise<void> {
        if (['didReceiveSettings', 'didReceiveGlobalSettings'].includes(event.event)) this.reset();
        await super.handle(client, event);
        if (event.event === 'willDisappear') {
            this.resources.unwatch(event.context ?? '');
            this.messages.delete(event.context ?? '');
            if (!this.bindings.size) this.reset();
        }
        if (
            event.event === 'propertyInspectorDidDisappear' &&
            !this.bindings.has(event.context ?? '')
        ) {
            this.queue = this.queue.filter((q) => {
                if (q.context !== event.context) return true;
                q.receipt.finish(Error('Cancelled'));
                return false;
            });
            if (this.activeContext === event.context) this.reset();
        }
    }
    // Run connector work whose deadline has arrived
    poll(client: Client): void {
        if (this.busy && this.activeDeadline !== undefined && this.clock() >= this.activeDeadline) {
            this.cancelJob('connector-work');
            this.activeReceipt?.finish(Error('Connector deadline exceeded'));
            if (this.activeResource)
                this.resources.complete(
                    this.activeResource.key,
                    this.activeResource.generation,
                    undefined,
                    Error('Connector deadline exceeded'),
                    this.clock(),
                    0.5,
                );
            this.activeReceipt = undefined;
            this.activeDeadline = undefined;
            this.activeResource = undefined;
            this.busy = false;
        }
        if (this.driver.changed?.())
            for (const state of this.resources.states.values()) state.next = 0;
        this.start(client);
        super.poll(client);
    }
    // Return the earliest connector work deadline
    nextPoll(now: number): number | undefined {
        if (this.busy) return this.activeDeadline;
        if (this.queue.length) return now;
        this.watch();
        const values = [...this.resources.states.keys()]
            .map((key) => this.resources.deadline(key))
            .filter((next) => next !== NEVER)
            .map((next) => next || now);
        return values.length ? Math.min(...values) : undefined;
    }
    // Combine adjacent dial input before issuing connector work
    batchDial(action: string): boolean {
        return !!this.options.dialActions?.includes(action.split('.').at(-1) ?? action);
    }
    // Clear transient connector state for a new owner
    private reset(): void {
        this.cancelJob('connector-work');
        this.activeReceipt?.finish(Error('Cancelled'));
        this.activeReceipt = undefined;
        for (const q of this.queue) q.receipt.finish(Error('Cancelled'));
        this.queue = [];
        this.busy = false;
        this.activeDeadline = undefined;
        this.activeResource = undefined;
        this.resources = new Resources();
        this.messages.clear();
        this.driver.close();
    }
    // Release connector jobs and subscriptions
    close(): void {
        this.reset();
        super.close();
    }
}
