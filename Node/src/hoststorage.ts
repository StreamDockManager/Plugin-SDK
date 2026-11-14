import { UnsupportedFeatureError } from './protocol.js';
import type { Settings } from './protocol.js';
import type { Client } from './runtime.js';
import { byteLength } from './internal/bytes.js';

import { RPCError, control, encoded } from './control.js';
export {
    Control,
    RPCError,
    operationID,
    RPC_REQUEST,
    RPC_RESPONSE,
    MAX_STORAGE_MESSAGE,
} from './control.js';

export const STORAGE_PROTOCOL = 'plugin-storage-v1';
export const SETTINGS_PROTOCOL = 'plugin-settings-rpc-v1';
export const MAX_STORAGE_VALUE = 256 << 10;

export interface SettingsResult {
    settings?: Settings;
    backend: string;
    assurance: string;
}
// Validate the enhanced host-storage response envelope
function enhancedResult(result: any, method: string): SettingsResult {
    if (
        !result ||
        typeof result !== 'object' ||
        Array.isArray(result) ||
        (method === 'get' &&
            (result.assurance !== 'observed' ||
                !result.settings ||
                typeof result.settings !== 'object' ||
                Array.isArray(result.settings))) ||
        (method === 'replace' && !['committed', 'applied_to_draft'].includes(result.assurance))
    )
        throw new RPCError('invalid_response', 'Invalid enhanced settings result');
    return { ...result, backend: SETTINGS_PROTOCOL };
}
export class PortableSettings {
    #tail: Promise<unknown> = Promise.resolve();
    #queued = 0;
    // Bind portable action and global settings to a host client
    constructor(
        readonly client: Client,
        readonly scope: 'action' | 'global',
        readonly context = '',
    ) {
        if (!['action', 'global'].includes(scope) || (scope === 'action' && !context))
            throw Error('An owned action context or global scope is required');
        control(client);
    }
    // Serialize concurrent operations on this storage view
    #serialize<T>(work: () => Promise<T>): Promise<T> {
        if (this.#queued >= 8)
            return Promise.reject(new RPCError('busy', 'Settings queue is full'));
        this.#queued++;
        const result = this.#tail.then(work, work).finally(() => {
            this.#queued--;
        });
        this.#tail = result.catch(() => {});
        return result;
    }
    // Read the current host-owned settings value
    async #read(signal?: AbortSignal): Promise<SettingsResult> {
        if (this.client.supports(SETTINGS_PROTOCOL)) {
            try {
                return enhancedResult(
                    await control(this.client).request(
                        SETTINGS_PROTOCOL,
                        'get',
                        { scope: this.scope, context: this.context },
                        signal,
                    ),
                    'get',
                );
            } catch (error) {
                if (!(error instanceof RPCError) || error.code !== 'unsupported') throw error;
            }
        }
        return {
            settings: await control(this.client).read(this.scope, this.context, signal),
            backend: 'base-vsd',
            assurance: 'observed',
        };
    }
    // Read the current host-owned settings value
    read(signal?: AbortSignal): Promise<SettingsResult> {
        return this.#serialize(() => this.#read(signal));
    }
    // Replace the selected settings value through the host
    async #replace(settings: Settings, signal?: AbortSignal): Promise<SettingsResult> {
        if (!settings || typeof settings !== 'object' || Array.isArray(settings))
            throw Error('Settings must be an object');
        if (signal?.aborted)
            throw new RPCError('cancelled_before_commit', 'Request cancelled before enqueue');
        if (this.client.supports(SETTINGS_PROTOCOL)) {
            try {
                return enhancedResult(
                    await control(this.client).request(
                        SETTINGS_PROTOCOL,
                        'replace',
                        { scope: this.scope, context: this.context, settings },
                        signal,
                    ),
                    'replace',
                );
            } catch (error) {
                if (!(error instanceof RPCError) || error.code !== 'unsupported') throw error;
            }
        }
        if (this.scope === 'global') this.client.globalSettings(settings);
        else this.client.settings(this.context, settings);
        return { backend: 'base-vsd', assurance: 'sent' };
    }
    // Replace the selected settings value through the host
    replace(settings: Settings, signal?: AbortSignal): Promise<SettingsResult> {
        const snapshot = structuredClone(settings);
        return this.#serialize(() => this.#replace(snapshot, signal));
    }
    // Apply an update against the latest host-owned value
    update(changes: Settings, signal?: AbortSignal): Promise<SettingsResult> {
        const snapshot = structuredClone(changes);
        return this.#serialize(async () =>
            this.#replace({ ...(await this.#read(signal)).settings, ...snapshot }, signal),
        );
    }
}
export interface StorageVersion {
    generation: string;
    revision: string;
}
export interface StorageValue extends StorageVersion {
    found: boolean;
    value?: any;
}
export interface StorageKeys extends StorageVersion {
    keys: string[];
    cursor?: string;
}
export interface StorageReceipt extends Partial<StorageVersion> {
    generation: string;
    operation_id: string;
    committed?: boolean;
    found?: boolean;
}
export type StorageMutation =
    { op: 'put'; key: string; value: any } | { op: 'delete'; key: string };
// Validate a decimal protocol counter without losing precision
const decimal = (value: string) => typeof value === 'string' && /^(0|[1-9][0-9]*)$/.test(value);
// Validate the storage generation identifier
const generation = (value: string) => decimal(value) && value.length <= 39;
// Validate the storage revision counter
const revision = (value: string) =>
    decimal(value) &&
    (value.length < 20 || (value.length === 20 && value <= '18446744073709551615'));
// Validate the operation receipt identifier
const uuid = (value: string) => /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(value);
// Validate a key before sending it to host storage
const key = (value: string, limit: number) => {
    if (typeof value !== 'string' || !value || byteLength(value) > limit)
        throw Error(`Storage name must be 1–${limit} UTF-8 bytes`);
};
// No hidden local cache, generation adoption, retry, or weaker fallback
export class HostStore {
    // Bind the storage namespace to the negotiated host client
    constructor(
        readonly client: Client,
        readonly namespace: string,
    ) {
        if (!client.supports(STORAGE_PROTOCOL)) throw new UnsupportedFeatureError(STORAGE_PROTOCOL);
        key(namespace, 128);
        control(client);
    }
    // Issue a negotiated host-storage RPC
    async #call(method: string, payload: Settings, signal?: AbortSignal): Promise<any> {
        const result = await control(this.client).request(
            STORAGE_PROTOCOL,
            method,
            { ...payload, namespace: this.namespace },
            signal,
        );
        let valid =
            result &&
            typeof result === 'object' &&
            !Array.isArray(result) &&
            generation(result.generation);
        if (
            valid &&
            (method !== 'receipt' || result.found === true || Object.hasOwn(result, 'revision'))
        )
            valid = revision(result.revision);
        if (valid && (method === 'get' || method === 'receipt'))
            valid = typeof result.found === 'boolean';
        if (valid && method === 'get') valid = Object.hasOwn(result, 'value') === result.found;
        if (valid && method === 'listKeys')
            valid =
                Array.isArray(result.keys) &&
                result.keys.length <= 128 &&
                result.keys.every(
                    (name: unknown) =>
                        typeof name === 'string' && name.length && byteLength(name) <= 256,
                ) &&
                (result.cursor === undefined ||
                    (typeof result.cursor === 'string' && byteLength(result.cursor) <= 512));
        if (valid && (method === 'commit' || method === 'receipt'))
            valid =
                result.operation_id === payload.operation_id &&
                ((method === 'receipt' && result.found === false) || result.committed === true);
        if (!valid) throw new RPCError('invalid_response', 'Invalid storage result');
        return result;
    }
    // Retrieve one key and its current storage metadata
    get(name: string, signal?: AbortSignal): Promise<StorageValue> {
        key(name, 256);
        return this.#call('get', { key: name }, signal);
    }
    // Enumerate keys within the selected host namespace
    listKeys(cursor?: string, signal?: AbortSignal): Promise<StorageKeys> {
        if (cursor !== undefined && byteLength(cursor) > 512)
            throw Error('Cursor exceeds 512 bytes');
        return this.#call('listKeys', cursor ? { cursor } : {}, signal);
    }
    // Commit a bounded set of host-storage mutations
    commit(
        version: StorageVersion,
        operation: string,
        mutations: StorageMutation[],
        signal?: AbortSignal,
    ): Promise<StorageReceipt> {
        if (!generation(version.generation) || !revision(version.revision) || !uuid(operation))
            throw Error('Canonical generation, revision and operation ID required');
        if (!mutations.length || mutations.length > 32)
            throw Error('Commit requires 1–32 mutations');
        for (const mutation of mutations) {
            key(mutation.key, 256);
            if (mutation.op === 'put') {
                const value = Object.hasOwn(mutation, 'value')
                    ? encoded(mutation.value)
                    : undefined;
                if (value === undefined) throw Error('Put requires a JSON value');
                if (byteLength(value) > MAX_STORAGE_VALUE)
                    throw new RPCError('quota', 'Value exceeds 256 KiB');
            } else if (mutation.op !== 'delete') throw Error('Mutation must be put or delete');
        }
        return this.#call(
            'commit',
            {
                generation: version.generation,
                expected_revision: version.revision,
                operation_id: operation,
                mutations: structuredClone(mutations),
            },
            signal,
        );
    }
    // Retrieve the result of a previously submitted commit
    receipt(epoch: string, operation: string, signal?: AbortSignal): Promise<StorageReceipt> {
        if (!generation(epoch) || !uuid(operation))
            throw Error('Canonical generation and operation ID required');
        return this.#call('receipt', { generation: epoch, operation_id: operation }, signal);
    }
}
