export const UPDATE_INTERVAL_KEY = 'update_interval_ms';
export const NEVER = Number.MAX_SAFE_INTEGER;
export interface RefreshPolicy {
    default_ms: number;
    minimum_ms: number;
    maximum_ms: number;
}
// Resolve the requested polling interval within policy
export function resolveInterval(settings: Record<string, unknown>, policy: RefreshPolicy): number {
    const { default_ms, minimum_ms, maximum_ms } = policy;
    if (
        ![default_ms, minimum_ms, maximum_ms].every(Number.isSafeInteger) ||
        minimum_ms < 1 ||
        maximum_ms < minimum_ms ||
        maximum_ms > 86400000 ||
        (default_ms !== 0 && (default_ms < minimum_ms || default_ms > maximum_ms))
    )
        throw Error('Invalid refresh policy');
    const value = Object.hasOwn(settings, UPDATE_INTERVAL_KEY)
        ? settings[UPDATE_INTERVAL_KEY]
        : default_ms;
    if (
        typeof value !== 'number' ||
        !Number.isSafeInteger(value) ||
        value < 0 ||
        (value !== 0 && (value < minimum_ms || value > maximum_ms))
    )
        throw Error('Update interval is outside the supported range');
    return value;
}
// Update the effective polling interval
export function updateInterval(settings: Record<string, unknown>, fallback: number): number {
    try {
        return resolveInterval(settings, {
            default_ms: fallback,
            minimum_ms: 1,
            maximum_ms: 86400000,
        });
    } catch {
        return fallback;
    }
}
// Calculate the next scheduled polling time
export function nextUpdate(now: number, interval: number): number {
    return interval === 0 ? NEVER : now + interval;
}
// Calculate a bounded retry delay
export function retryDelay(interval: number, failures: number, jitter: number): number {
    return Math.max(
        interval,
        Math.min(
            900000,
            Math.min(900000, 2000 * 2 ** Math.min(Math.max(failures - 1, 0), 10)) *
                (1 + 0.25 * Math.min(1, Math.max(0, jitter))),
        ),
    );
}
// Largest whole-second Go duration, expressed in milliseconds for timestamp parity
const maximumRetryAfter = 9223372036000;
// Interpret the server-provided retry deadline
export function retryAfter(value: string, now = Date.now()): number | undefined {
    value = value.replace(/^[ \t\r\n]+|[ \t\r\n]+$/g, '');
    if (/^[0-9]+$/.test(value))
        return now + Math.min(Number(value), maximumRetryAfter / 1000) * 1000;
    const canonical = httpDate(value);
    if (!canonical) return undefined;
    const date = Date.parse(canonical);
    // Exact round-trip rejects impossible dates, time fields and mismatched weekdays
    if (!Number.isFinite(date) || new Date(date).toUTCString() !== canonical) return undefined;
    return now + Math.min(maximumRetryAfter, Math.max(0, date - now));
}
// Parse an HTTP date for retry scheduling
function httpDate(value: string): string | undefined {
    let parts: string[] | null =
        /^((?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)), ([0-9]{2}) ((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)) ([0-9]{4}) ([0-9]{2}:[0-9]{2}:[0-9]{2}) GMT$/.exec(
            value,
        );
    if (!parts) {
        parts =
            /^((?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday)), ([0-9]{2})-((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec))-([0-9]{2}) ([0-9]{2}:[0-9]{2}:[0-9]{2}) GMT$/.exec(
                value,
            );
        if (parts) parts[4] = String(Number(parts[4]) + (Number(parts[4]) < 70 ? 2000 : 1900));
        else {
            const old =
                /^((?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)) ((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)) ([ 0-9][0-9]) ([0-9]{2}:[0-9]{2}:[0-9]{2}) ([0-9]{4})$/.exec(
                    value,
                );
            if (old) parts = ['', old[1], old[3].trim().padStart(2, '0'), old[2], old[5], old[4]];
        }
    }
    if (!parts || Number(parts[4]) < 1970) return undefined;
    return `${parts[1].slice(0, 3)}, ${parts[2]} ${parts[3]} ${parts[4]} ${parts[5]} GMT`;
}
export class PollState {
    next = 0;
    failures = 0;
    lastValue: any;
    stale = false;
    observedAt?: number;
    // Record a completed poll and schedule its next attempt
    complete(
        value: any,
        error: unknown,
        now: number,
        interval: number,
        jitter = 0,
        retryAt = 0,
    ): void {
        if (error) {
            this.failures = Math.min(16, this.failures + 1);
            this.stale = this.observedAt !== undefined;
            this.next = now + retryDelay(interval, this.failures, jitter);
        } else {
            this.failures = 0;
            this.stale = false;
            this.lastValue = structuredClone(value);
            this.observedAt = now;
            this.next = nextUpdate(now, interval);
        }
        this.next = Math.max(this.next, retryAt);
    }
}
export class Resources {
    readonly states = new Map<string, PollState>();
    private bindings = new Map<string, { key: string; interval: number; state: PollState }>();
    private pending = new Map<string, number>();
    private cooldowns = new Map<string, number>();
    private sequence = 0;
    // Register demand for a polling key
    watch(context: string, key: string, interval: number): void {
        if (!key || !Number.isFinite(interval) || interval < 0)
            throw Error('Resource needs key and nonnegative interval');
        if (this.bindings.get(context)?.key !== key) this.unwatch(context);
        const prior = this.bindings.get(context);
        const state = prior?.state ?? structuredClone(this.states.get(key) ?? new PollState());
        if ((!prior || prior.interval !== interval) && state.observedAt !== undefined)
            state.next = nextUpdate(state.observedAt, interval);
        this.bindings.set(context, { key, interval, state });
        if (!this.states.has(key)) this.states.set(key, new PollState());
        if ((!prior || prior.interval !== interval) && this.states.get(key)!.next !== 0)
            this.schedule(key);
    }
    // Remove polling demand from a departing owner
    unwatch(context: string): void {
        const prior = this.bindings.get(context);
        this.bindings.delete(context);
        if (prior && ![...this.bindings.values()].some((x) => x.key === prior.key)) {
            this.states.delete(prior.key);
            this.pending.delete(prior.key);
            this.cooldowns.delete(prior.key);
        } else if (prior && this.states.get(prior.key)?.next !== 0) this.schedule(prior.key);
    }
    // Select watched keys whose deadlines have arrived
    due(now: number): string[] {
        return [...this.states]
            .filter(
                ([key, state]) =>
                    !this.pending.has(key) &&
                    now >= state.next &&
                    now >= (this.cooldowns.get(key) ?? 0),
            )
            .map(([key]) => key)
            .sort();
    }
    // Report the earliest pending polling deadline
    deadline(key: string): number {
        const state = this.states.get(key);
        return state ? Math.max(state.next, this.cooldowns.get(key) ?? 0) : NEVER;
    }
    // Mark a polling attempt as active
    begin(key: string): number {
        if (!this.states.has(key) || this.pending.has(key))
            throw Error('Resource absent or already running');
        const generation = ++this.sequence;
        this.pending.set(key, generation);
        return generation;
    }
    // Record a completed poll and schedule its next attempt
    complete(
        key: string,
        generation: number,
        value: any,
        error: unknown,
        now: number,
        jitter = 0,
        retryAt = 0,
    ): boolean {
        if (this.pending.get(key) !== generation || !this.states.has(key)) return false;
        this.pending.delete(key);
        if (retryAt > now) this.cooldowns.set(key, retryAt);
        else this.cooldowns.delete(key);
        const forced = this.states.get(key)!.next === 0;
        for (const binding of this.bindings.values()) {
            if (binding.key !== key || (!forced && now < binding.state.next)) continue;
            PollState.prototype.complete.call(
                binding.state,
                value,
                error,
                now,
                binding.interval,
                jitter,
                retryAt,
            );
            if (binding.interval === 0) binding.state.next = NEVER;
        }
        this.states.get(key)!.complete(value, error, now, 0, jitter, retryAt);
        this.schedule(key);
        return true;
    }
    // Set the next attempt time for a polling key
    private schedule(key: string): void {
        const state = this.states.get(key);
        if (state)
            state.next = Math.max(
                this.cooldowns.get(key) ?? 0,
                Math.min(
                    NEVER,
                    ...[...this.bindings.values()]
                        .filter((x) => x.key === key)
                        .map((x) => x.state.next),
                ),
            );
    }
    // Return current polling state without sharing mutable records
    snapshot(context: string): PollState | undefined {
        const binding = this.bindings.get(context);
        return binding ? structuredClone(binding.state) : undefined;
    }
}
