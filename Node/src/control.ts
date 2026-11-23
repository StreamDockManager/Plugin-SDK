import { finiteJSONValue } from './internal/json.js';
import { HostInfo, UnsupportedFeatureError } from './protocol.js';
import type { Event, Settings } from './protocol.js';
import type { Client } from './runtime.js';
import { byteLength } from './internal/bytes.js';

export const RPC_REQUEST = 'streamDockManagerRpcRequest';
export const RPC_RESPONSE = 'streamDockManagerRpcResponse';
export const MAX_STORAGE_MESSAGE = 512 << 10;
export class RPCError extends Error {
    // Preserve the host RPC error code alongside its message
    constructor(
        public code: string,
        message: string,
    ) {
        super(message);
    }
}
const privateServiceCodes = new Set([
    'needs_setup',
    'vault_locked',
    'permission_denied',
    'not_declared',
    'operation_unavailable',
    'unsupported',
    'invalid_request',
    'invalid_response',
    'busy',
    'quota',
    'timeout',
    'stale_revision',
    'slot_conflict',
    'cancelled',
    'rate_limited',
    'cancelled_before_commit',
    'indeterminate',
    'activation_required',
    'activation_expired',
    'no_text',
    'unsupported_format',
    'invalid_encoding',
    'too_large',
    'clipboard_changed',
    'clipboard_access',
]);
export function privateError(error: unknown): never {
    if (error instanceof UnsupportedFeatureError) throw error;
    const code =
        error instanceof RPCError && privateServiceCodes.has(error.code)
            ? error.code
            : 'operation_unavailable';
    throw new RPCError(code, 'Manager request did not complete');
}

interface Pending {
    protocol?: string;
    // Settle a control request and release its pending resources
    finish(value?: any, error?: unknown): void;
}
// Serialize a control message using the bounded wire format
export const encoded = (value: any) => JSON.stringify(value, finiteJSONValue());
// Create an identifier for a correlated host operation
export const operationID = (): string => crypto.randomUUID();
// Return the control client associated with this host
export function control(client: Client): Control {
    return (client.control ??= new Control(client.host, client.identity, (event) =>
        client.send(event.event, event.context, event.payload, undefined, event),
    ));
}

// Transport-owned correlation. Resolving a waiter never dispatches a user event
export class Control {
    #requests = new Map<string, Pending>();
    #reads = new Map<string, Pending>();
    #closed = false;
    // Bind correlated RPC and settings requests to this host identity
    constructor(
        readonly host: HostInfo,
        readonly identity: string,
        readonly send: (event: Event) => void,
    ) {}
    // Send a negotiated RPC request with bounded admission
    request(
        protocol: string,
        method: string,
        payload: Settings,
        signal?: AbortSignal,
    ): Promise<any> {
        if (!this.host.supports(protocol))
            return Promise.reject(new UnsupportedFeatureError(protocol));
        const id = operationID();
        const event = {
            event: RPC_REQUEST,
            protocol_id: protocol,
            request_id: id,
            method,
            payload,
        };
        if (byteLength(encoded(event)) > MAX_STORAGE_MESSAGE)
            return Promise.reject(new RPCError('quota', 'Storage request exceeds 512 KiB'));
        return this.#wait(this.#requests, id, event, protocol, signal);
    }
    // Request action or global settings for the selected scope
    read(scope: 'action' | 'global', context: string, signal?: AbortSignal): Promise<Settings> {
        const key = scope === 'global' ? 'global' : `action:${context}`;
        return this.#wait(
            this.#reads,
            key,
            {
                event: scope === 'global' ? 'getGlobalSettings' : 'getSettings',
                context: scope === 'global' ? this.identity : context,
            },
            undefined,
            signal,
        );
    }
    // Reserve a pending slot and wait for its correlated response
    #wait(
        map: Map<string, Pending>,
        id: string,
        event: Event,
        protocol?: string,
        signal?: AbortSignal,
    ): Promise<any> {
        if (this.#closed || signal?.aborted)
            return Promise.reject(
                new RPCError('cancelled_before_commit', 'Request cancelled before enqueue'),
            );
        if (map.has(id) || this.#requests.size + this.#reads.size >= 8)
            return Promise.reject(
                new RPCError('busy', 'Request already pending or eight-request capacity exhausted'),
            );
        return new Promise((resolve, reject) => {
            // Cancel the pending host request before further processing
            const abort = () =>
                pending.finish(
                    undefined,
                    new RPCError(
                        'indeterminate',
                        'Cancelled after enqueue; reconcile before retrying',
                    ),
                );
            const timer = setTimeout(
                () =>
                    pending.finish(
                        undefined,
                        new RPCError(
                            'indeterminate',
                            'Request timed out; reconcile before retrying',
                        ),
                    ),
                5000,
            );
            let finished = false;
            const pending: Pending = {
                protocol,
                // Settle a control request and release its pending resources
                finish: (value, error) => {
                    if (finished) return;
                    finished = true;
                    clearTimeout(timer);
                    signal?.removeEventListener('abort', abort);
                    map.delete(id);
                    if (error) reject(error);
                    else resolve(value);
                },
            };
            map.set(id, pending);
            signal?.addEventListener('abort', abort, { once: true });
            try {
                this.send(event);
            } catch (error) {
                pending.finish(undefined, error);
            }
        });
    }
    // Match host replies to pending RPC or settings requests
    observe(event: Event, rawSize = 0): boolean {
        if (event.event === RPC_RESPONSE) {
            const pending = this.#requests.get(event.request_id ?? '');
            if (!pending) return true;
            if (
                event.protocol_id !== pending.protocol ||
                rawSize > MAX_STORAGE_MESSAGE ||
                byteLength(encoded(event)) > MAX_STORAGE_MESSAGE ||
                Object.hasOwn(event, 'result') === Object.hasOwn(event, 'error')
            )
                pending.finish(
                    undefined,
                    new RPCError('invalid_response', 'Invalid Manager RPC response'),
                );
            else if (Object.hasOwn(event, 'error')) {
                if (
                    !event.error ||
                    typeof event.error.code !== 'string' ||
                    typeof event.error.message !== 'string'
                )
                    pending.finish(
                        undefined,
                        new RPCError('invalid_response', 'Invalid Manager RPC error'),
                    );
                else pending.finish(undefined, new RPCError(event.error.code, event.error.message));
            } else pending.finish(structuredClone(event.result));
            return true;
        }
        const key =
            event.event === 'didReceiveGlobalSettings'
                ? 'global'
                : event.event === 'didReceiveSettings'
                  ? `action:${event.context ?? ''}`
                  : undefined;
        const value = event.payload?.settings;
        if (key) {
            const pending = this.#reads.get(key);
            if (value && typeof value === 'object' && !Array.isArray(value))
                pending?.finish(structuredClone(value));
            else
                pending?.finish(
                    undefined,
                    new RPCError('invalid_response', 'Settings response must be an object'),
                );
        }
        return false;
    }
    // Report whether host control requests remain outstanding
    get hasPending(): boolean {
        return this.#requests.size > 0 || this.#reads.size > 0;
    }
    // Reject pending control work and prevent new requests
    close(): void {
        this.#closed = true;
        for (const map of [this.#requests, this.#reads])
            for (const pending of [...map.values()])
                pending.finish(
                    undefined,
                    new RPCError('indeterminate', 'Host disconnected; reconcile before retrying'),
                );
    }
}
