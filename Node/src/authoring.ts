import { keyLayout, wideLayout } from './display-layouts.js';
import type { Client, Handler } from './runtime.js';
import type { Event, Settings } from './protocol.js';
import { redact as safeError } from './credentials.js';
import { merge } from './settings.js';
export interface Display {
    title?: string;
    image?: string;
    state?: number;
    layout?: string;
    feedback?: Settings;
}
// JSON value equality ignores object insertion order and preserves array order/types
function feedbackEqual(a: unknown, b: unknown): boolean {
    if (a === b) return true;
    if (a === null || b === null || typeof a !== 'object' || typeof b !== 'object') return false;
    if (Array.isArray(a) || Array.isArray(b))
        return (
            Array.isArray(a) &&
            Array.isArray(b) &&
            a.length === b.length &&
            a.every((value, index) => feedbackEqual(value, b[index]))
        );
    const left = a as Record<string, unknown>,
        right = b as Record<string, unknown>;
    const keys = Object.keys(left);
    return (
        keys.length === Object.keys(right).length &&
        keys.every((key) => Object.hasOwn(right, key) && feedbackEqual(left[key], right[key]))
    );
}
// Build the minimal host messages needed to update a display
export function displayMessages(
    context: string,
    previous: Display | undefined,
    desired: Display,
    rich = true,
    states = true,
): Event[] {
    const old = previous ?? {},
        known = !!previous,
        layout = !known || old.layout !== desired.layout,
        state = states && (!known || (old.state ?? 0) !== (desired.state ?? 0)),
        out: Event[] = [];
    // Append a context-scoped display update
    const send = (event: string, payload: Settings) => out.push({ event, context, payload });
    if (rich && layout && desired.layout) send('setFeedbackLayout', { layout: desired.layout });
    if (state) send('setState', { state: desired.state ?? 0 });
    if (
        (!known && desired.image) ||
        (known && (old.image ?? '') !== (desired.image ?? '')) ||
        (state && desired.image)
    )
        send('setImage', { image: desired.image ?? '', target: 0 });
    if (!known || (old.title ?? '') !== (desired.title ?? '') || state)
        send('setTitle', { title: desired.title ?? '', target: 0 });
    if (rich) {
        const feedback: Settings = {};
        for (const [key, value] of Object.entries(desired.feedback ?? {}))
            if (
                layout ||
                !Object.hasOwn(old.feedback ?? {}, key) ||
                !feedbackEqual(old.feedback?.[key], value)
            )
                feedback[key] = structuredClone(value);
        for (const key of Object.keys(old.feedback ?? {}))
            if (!layout && !Object.hasOwn(desired.feedback ?? {}, key)) feedback[key] = null;
        if (Object.keys(feedback).length) send('setFeedback', feedback);
    }
    return out;
}
export class Renderer {
    readonly last = new Map<string, Display>();
    // Forget rendered state for one binding or every binding
    reset(context?: string): void {
        if (context === undefined) this.last.clear();
        else this.last.delete(context);
    }
    // Publish the desired display and remember its last state
    render(client: Client, context: string, desired: Display): void {
        for (const message of displayMessages(context, this.last.get(context), desired, true))
            client.send(message.event, context, message.payload);
        this.last.set(context, structuredClone(desired));
    }
}
export interface Binding {
    action: string;
    settings: Settings;
    surface?: Settings;
}
export interface ViewTarget {
    action: string;
    updatedBy?: string[];
    shared?: boolean;
    live?: boolean;
    afterActivation?: boolean;
}
export interface Scope {
    origin?: string;
    action?: string;
    automatic?: boolean;
    lifecycle?: boolean;
}
// Decide whether a settings update invalidates this target
export function canUpdate(
    bindings: Map<string, Binding>,
    targets: Map<string, ViewTarget>,
    active: Set<string>,
    context: string,
    scope: Scope,
    globals: Settings = {},
): boolean {
    const binding = bindings.get(context);
    if (!binding) return false;
    const rule = targets.get(binding.action),
        live = (rule?.live ?? true) && (!rule?.afterActivation || active.has(context)),
        source = bindings.get(scope.origin ?? '');
    if (scope.origin && !scope.automatic && scope.action && source?.action !== scope.action)
        return false;
    if (scope.origin === context) return !scope.lifecycle || live || active.has(context);
    if (scope.automatic) return live;
    if (
        !scope.origin ||
        !rule?.updatedBy?.includes(scope.action ?? '') ||
        (rule.afterActivation && !active.has(context))
    )
        return false;
    if (rule.shared) {
        const a = source && merge(globals, source.settings).shared_id,
            b = merge(globals, binding.settings).shared_id;
        return typeof a === 'string' && !!a && a === b;
    }
    return scope.action !== binding.action;
}
export interface Action {
    handle?: (plugin: Plugin, client: Client, event: Event) => void | Promise<void>;
    appeared?: Action['handle'];
    changed?: Action['handle'];
    disappeared?: Action['handle'];
    render?: (plugin: Plugin, context: string, binding: Binding) => Display;
}
export class Plugin implements Handler {
    shouldLogAction?: (event: Event) => boolean;
    readonly actions = new Map<string, Action>();
    readonly bindings = new Map<string, Binding>();
    readonly active = new Set<string>();
    readonly targets = new Map<string, ViewTarget>();
    readonly renderer = new Renderer();
    globals: Settings = {};
    scope: Scope = { automatic: true };
    inspector?: Action['handle'];
    private jobs = new Map<
        string,
        { generation: number; context: string; cancel: AbortController }
    >();
    private generation = 0;
    // Initialize action registration and binding-owned rendering state
    constructor(
        targets: ViewTarget[] = [],
        readonly clock = Date.now,
    ) {
        for (const target of targets) this.targets.set(target.action, target);
    }
    // Register an action handler and its rendering dependencies
    register(action: string, handler: Action): this {
        if (!action || this.actions.has(action)) throw Error('Empty or duplicate action');
        this.actions.set(action, handler);
        return this;
    }
    // Combine inherited and binding-specific settings for this action
    effectiveSettings(context: string): Settings {
        return merge(this.globals, this.bindings.get(context)?.settings ?? {});
    }
    // Publish the desired display and remember its last state
    render(client: Client, context: string): void {
        const binding = this.bindings.get(context);
        if (
            binding &&
            canUpdate(this.bindings, this.targets, this.active, context, this.scope, this.globals)
        ) {
            const view = this.actions.get(binding.action)?.render?.(this, context, binding);
            if (view) this.renderer.render(client, context, view);
        }
    }
    // Refresh the dependent visible targets after an action changes
    renderTargets(client: Client): void {
        for (const context of [...this.bindings.keys()].sort()) this.render(client, context);
    }
    // Retire cached binding state for a disappearing context
    clear(context: string): void {
        for (const [key, job] of this.jobs) if (job.context === context) this.cancelJob(key);
        this.renderer.reset(context);
    }
    // Route host events through action bindings and display updates
    async handle(client: Client, event: Event): Promise<void> {
        if (
            ['willAppear', 'didReceiveSettings', 'didReceiveGlobalSettings'].includes(event.event)
        ) {
            const snapshots = structuredClone([
                this.globals,
                event.payload?.settings ?? {},
                ...[...this.bindings.values()].map((binding) => binding.settings),
            ]);
            client.logRedactor = (text) =>
                snapshots.reduce(
                    (message, settings) => safeError(Error(message), settings).message,
                    text,
                );
        }
        const context = event.context ?? '',
            name = event.event,
            action = event.action ?? '',
            old = this.bindings.get(context),
            scope = this.scope,
            lifecycle = ['willAppear', 'didReceiveSettings'].includes(name);
        this.scope = { origin: context, action, automatic: lifecycle, lifecycle };
        try {
            if (name === 'didReceiveGlobalSettings') {
                this.globals = structuredClone(event.payload?.settings ?? {});
                for (const key of this.jobs.keys()) this.cancelJob(key);
                return;
            }
            if (name === 'didReceivePluginTheme') {
                const theme = event.payload?.resolved;
                if (!['light', 'dark'].includes(theme)) throw Error('Unsupported theme');
                for (const [id, view] of [...this.renderer.last]) {
                    this.renderer.reset(id);
                    this.renderer.render(client, id, themeAsset(view, theme));
                }
                return;
            }
            if (name === 'sendToPlugin' && this.inspector) {
                await this.inspector(this, client, event);
                return;
            }
            if (!context) return;
            if (old && lifecycle && old.action !== action) {
                this.clear(context);
                this.active.delete(context);
                await this.actions.get(old.action)?.disappeared?.(this, client, event);
            }
            if (name === 'willDisappear') {
                this.clear(context);
                this.bindings.delete(context);
                this.active.delete(context);
                await this.actions.get(old?.action ?? action)?.disappeared?.(this, client, event);
                return;
            }
            if (lifecycle)
                this.bindings.set(context, {
                    action,
                    settings: structuredClone(event.payload?.settings ?? {}),
                    surface: event.streamDockManager ?? old?.surface,
                });
            if (name === 'didReceiveSettings') this.clear(context);
            if (
                !this.bindings.has(context) &&
                ['keyDown', 'keyUp', 'dialDown', 'dialUp', 'dialRotate', 'touchTap'].includes(name)
            )
                this.bindings.set(context, {
                    action,
                    settings: structuredClone(event.payload?.settings ?? {}),
                    surface: event.streamDockManager,
                });
            const handlers = this.actions.get(action || old?.action || '');
            if (['keyDown', 'dialDown', 'touchTap'].includes(name)) this.active.add(context);
            await (
                name === 'willAppear'
                    ? handlers?.appeared
                    : name === 'didReceiveSettings'
                      ? handlers?.changed
                      : handlers?.handle
            )?.(this, client, event);
            if (lifecycle) this.render(client, context);
            else this.renderTargets(client);
        } finally {
            this.scope = scope;
        }
    }
    // Schedule binding-owned background work through the client
    background<T>(
        client: Client,
        key: string,
        context: string,
        work: (signal: AbortSignal) => Promise<T>,
        apply: (plugin: Plugin, client: Client, value: T) => void | Promise<void>,
        replace = false,
        trackAction = true,
    ): AbortController {
        if (this.jobs.has(key)) {
            if (!replace) throw Error('Plugin action already running');
            this.cancelJob(key);
        }
        if (this.jobs.size >= 32) throw Error('Plugin job queue full');
        const generation = ++this.generation,
            scope = { ...this.scope };
        const cancel = client.background(
            context,
            work,
            // Apply completed work only if the binding still owns this job
            async (value) => {
                if (this.jobs.get(key)?.generation !== generation) return;
                this.jobs.delete(key);
                const old = this.scope;
                this.scope = scope;
                try {
                    await apply(this, client, value);
                    this.renderTargets(client);
                } finally {
                    this.scope = old;
                }
            },
            trackAction,
        );
        this.jobs.set(key, { generation, context, cancel });
        return cancel;
    }
    /** A status read cannot change the initiating command's receipt. */
    observe<T>(
        client: Client,
        key: string,
        context: string,
        work: (signal: AbortSignal) => Promise<T>,
        apply: (
            plugin: Plugin,
            client: Client,
            result: { value?: T; error?: unknown },
        ) => void | Promise<void>,
    ): AbortController | undefined {
        try {
            return this.background(
                client,
                key,
                context,
                async (signal) => {
                    try {
                        return { value: await work(signal) };
                    } catch (error) {
                        return { error };
                    }
                },
                apply,
                false,
                false,
            );
        } catch {
            return undefined;
        }
    }
    // Cancel outstanding work owned by this binding
    cancelJob(key: string): void {
        this.jobs.get(key)?.cancel.abort();
        this.jobs.delete(key);
    }
    // Run due action work and refresh affected displays
    poll(client: Client): void {
        this.scope = { automatic: true };
        this.renderTargets(client);
    }
    // Report the next action deadline to the host scheduler
    nextPoll(_now: number): number | undefined {
        return undefined;
    }
    // Release action bindings and their outstanding work
    close(): void {
        for (const key of this.jobs.keys()) this.cancelJob(key);
    }
}
// Select artwork for the requested theme
export function themeAsset<T>(value: T, theme: string): T {
    if (typeof value === 'string')
        return value.replace(/-(light|dark)(\.(svg|json))$/, `-${theme}$2`) as T;
    if (Array.isArray(value)) return value.map((x) => themeAsset(x, theme)) as T;
    if (value && typeof value === 'object')
        return Object.fromEntries(
            Object.entries(value).map(([k, v]) => [k, themeAsset(v, theme)]),
        ) as T;
    return value;
}

// Choose a layout appropriate to the target control
export function displayLayout(wide = false): Settings {
    return JSON.parse(wide ? wideLayout : keyLayout);
}
export interface Visual {
    icon?: string;
    states?: string[];
}
export interface DisplayStatus {
    text: string;
    icon?: string;
    state?: number;
    unavailable?: boolean;
}
// Prepare the text lines for a control display
export function displayLines(text: string, columns: number): string[] {
    columns = Math.max(1, columns);
    const lines: string[] = [],
        paragraphs = text.split('\n');
    for (const paragraph of paragraphs) {
        let chars = Array.from(paragraph);
        if (paragraphs.length > 1 && chars.length > columns) {
            lines.push(chars.slice(0, Math.max(0, columns - 1)).join('') + '>');
            continue;
        }
        while (chars.length > columns) {
            let end = chars.slice(0, columns).lastIndexOf(' ');
            if (end <= 0) end = columns;
            lines.push(chars.slice(0, end).join(''));
            chars = Array.from(chars.slice(end).join('').trim());
        }
        lines.push(chars.join(''));
    }
    if (lines.length > 3) {
        lines.splice(3);
        const last = Array.from(lines[2]);
        lines[2] =
            (last.length >= columns ? last.slice(0, Math.max(0, columns - 1)) : last).join('') +
            '>';
    }
    return lines;
}
// Build a display from the current status and detail text
export function statusDisplay(
    status: DisplayStatus,
    surface?: Settings,
    visual: Visual = {},
    theme = '',
): Display {
    const states = visual.states ?? [];
    let state = status.state ?? 0;
    if (state < 0 || state >= states.length) state = 0;
    let icon = status.icon || (states.length > 1 ? states[state] : visual.icon);
    if (status.unavailable || !icon) {
        icon = 'icons/unavailable';
        state = 0;
    }
    const image = icon + (theme ? '-' + theme : '') + '.svg',
        width = surface?.width > 0 && surface?.height > 0 ? Math.min(16384, surface!.width) : 112,
        height = surface?.width > 0 && surface?.height > 0 ? Math.min(16384, surface!.height) : 112;
    let textWidth = Math.floor((width * 192) / 200),
        size = Math.min(Math.floor(width * 0.6), Math.floor(height * 0.43)),
        x = Math.floor((width - size) / 2),
        y = 0,
        layout = 'key';
    if (width > height * 1.3) {
        layout = 'wide';
        textWidth = Math.floor((width * 120) / 200);
        size = Math.min(Math.floor(width * 0.36), Math.floor(height * 0.8));
        x = Math.floor(width * 0.02);
        y = Math.floor((height - size) / 2);
    }
    const title = Array.from(status.text).slice(0, 500).join(''),
        feedback: Settings = {
            icon: {
                value: image,
                rect: [
                    Math.floor((x * 200) / width),
                    Math.floor((y * 100) / height),
                    Math.floor((size * 200) / width),
                    Math.floor((size * 100) / height),
                ],
            },
            indicator: { rect: [0, 0, 0, 0] },
        };
    const lines = displayLines(
        title,
        Math.max(
            1,
            Math.floor(textWidth / (8 * Math.min(6, Math.max(1, Math.floor(height / 100))))),
        ),
    );
    for (let i = 0; i < 3; i++) feedback['line' + i] = lines[i] ?? '';
    return {
        title,
        image,
        state,
        layout: 'display/' + layout + (theme ? '-' + theme : '') + '.json',
        feedback,
    };
}

export interface Presentation {
    kind: 'value' | 'command' | 'toggle' | 'media' | 'progress';
    primary?: string;
    label?: string;
    detail?: string;
    progress?: number;
    showProgress?: boolean;
}
/** Semantic layouts retain the full title and saved feedback element identities. */
export function present(
    view: Display,
    content: Presentation,
    width: number,
    height: number,
): Display {
    if (
        !content.kind ||
        !Number.isInteger(width) ||
        !Number.isInteger(height) ||
        width <= 0 ||
        height <= 0 ||
        width > 16384 ||
        height > 16384
    )
        return structuredClone(view);
    const result = structuredClone(view),
        feedback = (result.feedback ??= {});
    const primary = content.primary ?? '',
        label = content.label === primary ? '' : (content.label ?? '');
    const detail =
        content.detail === primary || content.detail === label ? '' : (content.detail ?? '');
    const wide = width > (height * 13) / 10;
    // Construct a feedback field value
    const field = (value: string, rect: number[], size: number) => ({
        value,
        rect: value ? rect : [rect[0], rect[1], 0, 0],
        font: { size, fit: true },
    });
    if (wide) {
        const icon = Math.min(Math.floor((width * 24) / 100), Math.floor((height * 70) / 100));
        feedback.icon = {
            value: result.image ?? '',
            rect: [
                4,
                Math.floor((100 - Math.floor((icon * 100) / height)) / 2),
                Math.floor((icon * 200) / width),
                Math.floor((icon * 100) / height),
            ],
        };
        feedback.line0 = field(primary, [58, 8, 138, 48], 32);
        feedback.line1 = field(label, [58, 58, 138, 18], 13);
        feedback.line2 = field(detail, [58, 78, 138, 16], 11);
    } else {
        feedback.icon = { value: result.image ?? '', rect: [76, 2, 48, 24] };
        feedback.line0 = field(primary, [6, 28, 188, 40], 30);
        feedback.line1 = field(label, [6, 70, 188, 14], 11);
        feedback.line2 = field(detail, [6, 85, 188, 11], 9);
    }
    if (content.kind === 'command' && !primary) {
        if (wide) {
            const icon = Math.min(Math.floor((width * 36) / 100), Math.floor((height * 80) / 100));
            feedback.icon = {
                value: result.image ?? '',
                rect: [
                    4,
                    Math.floor((100 - Math.floor((icon * 100) / height)) / 2),
                    Math.floor((icon * 200) / width),
                    Math.floor((icon * 100) / height),
                ],
            };
            feedback.line1 = field(label, [82, 20, 114, 60], 24);
        } else {
            feedback.icon = { value: result.image ?? '', rect: [45, 5, 110, 55] };
            feedback.line1 = field(label, [6, 67, 188, 26], 16);
        }
    }
    feedback.indicator = { rect: [0, 0, 0, 0] };
    if (content.showProgress && Number.isFinite(content.progress))
        feedback.indicator = {
            value: Math.max(0, Math.min(100, content.progress!)),
            rect: [6, 97, 188, 3],
        };
    return result;
}
// Build the compact form of a control display
export function compactDisplay(view: Display, width: number, height: number): Display {
    const result = structuredClone(view),
        feedback = result.feedback ?? {};
    if (
        !Number.isInteger(width) ||
        !Number.isInteger(height) ||
        width <= 0 ||
        height <= 0 ||
        height >= 80 ||
        width > Math.floor((height * 13) / 10)
    )
        return result;
    const primary = feedback.line0,
        label = feedback.line1,
        icon = feedback.icon;
    if (
        primary?.value === undefined ||
        primary?.value === null ||
        primary?.value === '' ||
        typeof label !== 'object' ||
        label === null ||
        typeof icon !== 'object' ||
        icon === null
    )
        return result;
    const size = Math.floor((Math.min(width, height) * 37) / 100);
    feedback.line2 = { rect: [0, 0, 0, 0] };
    icon.rect = [
        Math.floor(((width - size) * 100) / width),
        2,
        Math.floor((size * 200) / width),
        Math.floor((size * 100) / height),
    ];
    primary.rect = [7, 40, 186, 34];
    label.rect = [7, 75, 186, 21];
    label.font = { size: 19, fit: true };
    return result;
}
