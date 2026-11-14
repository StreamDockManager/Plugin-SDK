/** Event-loop-owned JSON records. Backends are explicit; no filesystem fallback. */
import { encodeJSON } from './internal/json.js';
import { createHash } from 'node:crypto';
import { isDeepStrictEqual } from 'node:util';
import {
    RPCError,
    operationID,
    type PortableSettings,
    type HostStore,
    type StorageVersion,
    type StorageMutation,
} from './hoststorage.js';
type PortableBackend = Pick<PortableSettings, 'read' | 'update'>;
type PrivateBackend = Pick<HostStore, 'listKeys' | 'get' | 'receipt' | 'commit'>;
type Snapshot = Record<string, unknown>;
// Validate the managed-state namespace key
const key = (identity: string) => 'record:' + createHash('sha256').update(identity).digest('hex');
const encoded = encodeJSON;
// Copy managed state without exposing mutable internal references
function snapshot(value: unknown): Snapshot {
    const text = encoded(value);
    if (text === undefined || Buffer.byteLength(text) > 8 << 20)
        throw new RPCError('quota', 'Managed state exceeds 8 MiB');
    const result = JSON.parse(text);
    if (
        !result ||
        Array.isArray(result) ||
        typeof result !== 'object' ||
        Object.keys(result).some((k) => !k)
    )
        throw Error('Managed state must be a JSON object with nonempty record identities');
    if (Object.keys(result).length > 4096)
        throw new RPCError('quota', 'Managed state exceeds 4096 records');
    return result;
}
export class ManagedState {
    dirty = false;
    private marks = 0;
    assurance = '';
    private ready = false;
    private busy = false;
    private written: Snapshot = {};
    private version: StorageVersion = { generation: '0', revision: '0' };
    private blocked?: unknown;
    private pending?: {
        id: string;
        version: StorageVersion;
        mutations: StorageMutation[];
        snapshot: Snapshot;
    };
    // Bind the managed namespace to its version and persistence backend
    constructor(
        readonly mode: 'session' | 'portable' | 'private',
        private backend?: PortableBackend | PrivateBackend,
        private portableKey = '_runtime_v1',
    ) {
        if (
            !['session', 'portable', 'private'].includes(mode) ||
            (mode !== 'session' && !backend) ||
            (mode === 'portable' && !portableKey)
        )
            throw Error('Managed state requires a valid mode and explicit backend');
    }
    // Serialize managed-state updates and persistence
    private async exclusive<T>(work: () => Promise<T>): Promise<T> {
        if (this.busy) throw new RPCError('busy', 'Managed state operation already pending');
        this.busy = true;
        try {
            return await work();
        } finally {
            this.busy = false;
        }
    }
    // Load managed state and reconcile its stored revision
    load(signal?: AbortSignal): Promise<Snapshot> {
        return this.exclusive(
            // Load and reconcile the snapshot without interleaving another state operation
            async () => {
                if (this.ready) return snapshot(this.written);
                let value: Snapshot = {};
                if (this.mode === 'portable') {
                    const result = await (this.backend as PortableBackend).read(signal);
                    this.assurance = result.assurance;
                    if (Object.hasOwn(result.settings ?? {}, this.portableKey))
                        value = snapshot(result.settings![this.portableKey]);
                } else if (this.mode === 'private') {
                    const backend = this.backend as PrivateBackend,
                        seen = new Set<string>();
                    let cursor = '',
                        total = 0;
                    while (true) {
                        if (seen.has(cursor) || seen.size >= 4096)
                            throw Error('Repeated storage cursor or restore page budget exceeded');
                        seen.add(cursor);
                        const page = await backend.listKeys(cursor, signal);
                        if (!cursor)
                            this.version = { generation: page.generation, revision: page.revision };
                        else if (
                            page.generation !== this.version.generation ||
                            page.revision !== this.version.revision
                        )
                            throw new RPCError('conflict', 'Private state changed during load');
                        for (const name of page.keys) {
                            const item = await backend.get(name, signal);
                            if (
                                !item.found ||
                                item.generation !== this.version.generation ||
                                item.revision !== this.version.revision
                            )
                                throw new RPCError('conflict', 'Private state changed during load');
                            total += Buffer.byteLength(encoded(item.value));
                            if (total > 8 << 20 || Object.keys(value).length >= 4096)
                                throw new RPCError('quota', 'Managed state exceeds restore budget');
                            const record = item.value;
                            if (
                                !record ||
                                typeof record.identity !== 'string' ||
                                !record.identity ||
                                !Object.hasOwn(record, 'value') ||
                                key(record.identity) !== name ||
                                Object.hasOwn(value, record.identity)
                            )
                                throw Error('Invalid managed state record');
                            Object.defineProperty(value, record.identity, {
                                value: record.value,
                                enumerable: true,
                                writable: true,
                                configurable: true,
                            });
                        }
                        cursor = page.cursor ?? '';
                        if (!cursor) break;
                    }
                    this.assurance = 'observed';
                } else this.assurance = 'session';
                this.written = snapshot(value);
                this.ready = true;
                return snapshot(value);
            },
        );
    }
    // Mark the in-memory value as needing persistence
    markDirty() {
        this.marks++;
        this.dirty = true;
    }
    // Resolve an outstanding commit before submitting another
    checkPending() {
        if (this.dirty && this.mode !== 'session')
            throw Error('Managed state has an unconfirmed write');
    }
    // Persist the current managed-state snapshot
    save(value: Snapshot, signal?: AbortSignal): Promise<void> {
        const marks = this.marks;
        return this.exclusive(
            // Resolve pending receipts before committing the next snapshot
            async () => {
                if (!this.ready) throw Error('Managed state is not initialized');
                if (this.blocked) throw this.blocked;
                const current = snapshot(value);
                this.dirty = true;
                if (this.mode === 'session') {
                    this.written = current;
                    this.dirty = false;
                    return;
                }
                if (this.mode === 'portable') {
                    if (!isDeepStrictEqual(current, this.written)) {
                        if (Buffer.byteLength(encoded(current)) > 256 << 10)
                            throw new RPCError('quota', 'Portable runtime state exceeds 256 KiB');
                        const result = await (this.backend as PortableBackend).update(
                            { [this.portableKey]: current },
                            signal,
                        );
                        this.assurance = result.assurance;
                    }
                    this.written = current;
                    this.dirty = false;
                    return;
                }
                const backend = this.backend as PrivateBackend;
                if (this.pending) {
                    const receipt = await backend.receipt(
                        this.pending.version.generation,
                        this.pending.id,
                        signal,
                    );
                    if (receipt.found && receipt.committed) {
                        this.version = {
                            generation: receipt.generation,
                            revision: receipt.revision!,
                        };
                        this.assurance = 'committed';
                        this.written = this.pending.snapshot;
                        this.pending = undefined;
                    }
                }
                if (!this.pending) {
                    const keys = [
                        ...new Set([...Object.keys(current), ...Object.keys(this.written)]),
                    ]
                        .filter(
                            (k) =>
                                Object.hasOwn(current, k) !== Object.hasOwn(this.written, k) ||
                                !isDeepStrictEqual(current[k], this.written[k]),
                        )
                        .sort();
                    if (keys.length > 32)
                        throw new RPCError(
                            'quota',
                            'Managed state change exceeds one atomic 32-record commit',
                        );
                    if (!keys.length) {
                        this.dirty = false;
                        return;
                    }
                    const mutations: StorageMutation[] = keys.map((identity) =>
                        Object.hasOwn(current, identity)
                            ? {
                                  op: 'put',
                                  key: key(identity),
                                  value: { identity, value: current[identity] },
                              }
                            : { op: 'delete', key: key(identity) },
                    );
                    for (const mutation of mutations)
                        if (
                            mutation.op === 'put' &&
                            Buffer.byteLength(encoded(mutation.value)) > 256 << 10
                        )
                            throw new RPCError('quota', 'Managed state record exceeds 256 KiB');
                    this.pending = {
                        id: operationID(),
                        version: { ...this.version },
                        mutations,
                        snapshot: current,
                    };
                }
                const pending = this.pending;
                try {
                    const receipt = await backend.commit(
                        pending.version,
                        pending.id,
                        pending.mutations,
                        signal,
                    );
                    this.version = { generation: receipt.generation, revision: receipt.revision! };
                    this.assurance = 'committed';
                    this.written = pending.snapshot;
                    this.pending = undefined;
                    this.dirty = !isDeepStrictEqual(current, this.written);
                } catch (error) {
                    if (error instanceof RPCError && error.code === 'conflict')
                        this.blocked = error;
                    throw error;
                }
            },
        ).finally(() => {
            if (this.marks !== marks) this.dirty = true;
        });
    }
}
