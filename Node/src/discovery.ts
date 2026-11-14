/** Owner-loop coordinator for read-only discovery. Schedule with the SDK's
 * bounded background executor; complete/expire on the owner loop, clear on unbind. */
import { encodeJSON } from './internal/json.js';
export interface Request {
    context: string;
    action?: string;
    channel: string;
    id: string;
    operation: string;
    scope?: 'action' | 'plugin';
}
export interface Reply {
    context: string;
    action: string;
    payload: Record<string, unknown>;
}
export interface Ticket {
    readonly request: Readonly<Request>;
    readonly deadline: number;
    readonly signal: AbortSignal;
}
// Validate the named discovery request field
const field = (s: unknown, max: number, empty = false) =>
    typeof s === 'string' &&
    (empty || s !== '') &&
    Buffer.byteLength(s) <= max &&
    Buffer.from(s).toString() === s;
// Reject unsupported or malformed discovery requests
function validate(r: Request) {
    if (
        !field(r.context, 1024) ||
        !field(r.action ?? '', 1024, true) ||
        !field(r.channel, 128) ||
        !field(r.id, 128, false) ||
        !field(r.operation, 128) ||
        !['action', 'plugin'].includes(r.scope ?? 'action')
    )
        throw Error('Invalid discovery request');
}
// Validate a discovery deadline value
const time = (n: number) => Number.isSafeInteger(n) && n >= 0;
export const MAX_PENDING = 32,
    MAX_RESPONSE_BYTES = 256 << 10;
// Construct the response envelope for inspector discovery
export function envelope(
    request: Request,
    payload: Record<string, unknown> = {},
    failure = '',
): Reply {
    validate(request);
    const result = {
        ...payload,
        op: request.operation,
        request_id: request.id,
        scope: request.scope ?? 'action',
        available: !failure,
    };
    if (failure) {
        Object.assign(result, { error: failure });
        if (!Object.hasOwn(result, 'message'))
            Object.assign(result, {
                message:
                    (
                        {
                            timeout: 'Discovery timed out',
                            busy: 'Discovery is busy; try again shortly',
                            failed: 'Discovery failed',
                        } as Record<string, string>
                    )[failure] ?? 'Discovery failed',
            });
    }
    let encoded: string;
    try {
        encoded = encodeJSON(result);
        if (Buffer.byteLength(encoded) > MAX_RESPONSE_BYTES) throw Error();
    } catch {
        return envelope(
            request,
            { message: 'Discovery response exceeds its supported size' },
            'response_too_large',
        );
    }
    return { context: request.context, action: request.action ?? '', payload: JSON.parse(encoded) };
}
export class Runner {
    private pending = new Map<string, { ticket: Ticket; cancel: AbortController }>();
    private closed = false;
    // Derive the identity used to coalesce discovery requests
    private key(context: string, channel: string) {
        return JSON.stringify([context, channel]);
    }
    // Begin a bounded discovery request for its owner
    start(request: Request, now: number, timeout: number): Ticket {
        validate(request);
        if (
            this.closed ||
            !time(now) ||
            !Number.isInteger(timeout) ||
            timeout < 1 ||
            timeout > 120000 ||
            !time(now + timeout)
        )
            throw Error('Invalid discovery deadline or closed runner');
        const key = this.key(request.context, request.channel);
        if (!this.pending.has(key) && this.pending.size >= MAX_PENDING)
            throw Error('Discovery queue full');
        this.pending.get(key)?.cancel.abort();
        const cancel = new AbortController(),
            ticket = Object.freeze({
                request: Object.freeze({ ...request, scope: request.scope ?? 'action' }),
                deadline: now + timeout,
                signal: cancel.signal,
            });
        this.pending.set(key, { ticket, cancel });
        return ticket;
    }
    // Cancel discovery work associated with a request
    cancel(context: string, channel: string, id: string): boolean {
        const key = this.key(context, channel),
            v = this.pending.get(key);
        if (!v || v.ticket.request.id !== id) return false;
        v.cancel.abort();
        this.pending.delete(key);
        return true;
    }
    // Discard discovery state for a departing owner
    clear(context: string) {
        for (const v of this.pending.values())
            if (v.ticket.request.context === context)
                this.cancel(context, v.ticket.request.channel, v.ticket.request.id);
    }
    // Cancel all discovery work and stop admission
    close() {
        for (const v of this.pending.values()) v.cancel.abort();
        this.pending.clear();
        this.closed = true;
    }
    // Return the earliest outstanding discovery deadline
    nextDeadline(): number {
        const next = Math.min(...[...this.pending.values()].map((v) => v.ticket.deadline));
        return next === Infinity ? 0 : next;
    }
    // Publish a discovery result only while its request remains current
    complete(
        ticket: Ticket,
        payload: Record<string, unknown>,
        failure: string,
        now: number,
    ): Reply | undefined {
        if (!time(now)) throw Error('Invalid discovery time');
        const key = this.key(ticket.request.context, ticket.request.channel),
            v = this.pending.get(key);
        if (v?.ticket !== ticket) return undefined;
        if (now >= ticket.deadline) {
            payload = {};
            failure = 'timeout';
        } else if (ticket.signal.aborted) {
            this.pending.delete(key);
            return undefined;
        }
        const reply = envelope(ticket.request, payload, failure);
        v.cancel.abort();
        this.pending.delete(key);
        return reply;
    }
    // Retire discovery requests whose deadlines have elapsed
    expire(now: number): Reply[] {
        if (!time(now)) throw Error('Invalid discovery time');
        return [...this.pending.values()]
            .map((v) => v.ticket)
            .filter((t) => t.deadline <= now)
            .sort(
                (a, b) =>
                    a.deadline - b.deadline ||
                    Buffer.compare(
                        Buffer.from(a.request.context),
                        Buffer.from(b.request.context),
                    ) ||
                    Buffer.compare(Buffer.from(a.request.channel), Buffer.from(b.request.channel)),
            )
            .map((t) => this.complete(t, {}, 'timeout', now)!)
            .filter(Boolean);
    }
}
