/** Owner-serialized LRU cache. Values are immutable caller-owned snapshots;
 * charged costs bound logical retained bytes, not process RSS. */
import { ManagedState } from './managedstate.js';
import type { HostStore } from './hoststorage.js';
import { encodeJSON } from './internal/json.js';
import { isDeepStrictEqual } from 'node:util';
export const MAX_ENTRIES = 65536,
    MAX_BYTES = 64 << 20,
    MAX_TIME = Number.MAX_SAFE_INTEGER;
export interface Entry<T> {
    key: string;
    value: T;
    cost: number;
    expires: number;
}
// Validate a cache timestamp before comparing deadlines
const time = (v: number) => Number.isSafeInteger(v) && v >= 0;
// Measure a cache key in UTF-8 bytes
const bytes = (s: string) => Buffer.byteLength(s);
// Order entries deterministically for eviction
const compare = (a: string, b: string) => Buffer.compare(Buffer.from(a), Buffer.from(b));
// Account for the key and value cost of a cache entry
const charge = <T>(e: Entry<T>) => e.cost + bytes(e.key) + 64;
export class Cache<T> {
    private values = new Map<string, Entry<T>>();
    private retained = 0;
    // Validate the entry-count and byte budgets for this cache
    constructor(
        readonly capacity: number,
        readonly limit: number,
    ) {
        if (
            !Number.isInteger(capacity) ||
            capacity < 1 ||
            capacity > MAX_ENTRIES ||
            !Number.isInteger(limit) ||
            limit < 1 ||
            limit > MAX_BYTES
        )
            throw Error('Invalid cache limits');
    }
    // Report the number of retained cache entries
    get size() {
        return this.values.size;
    }
    // Report the retained cache byte count
    get bytes() {
        return this.retained;
    }
    // Remove an entry and release its byte charge
    delete(key: string): boolean {
        const e = this.values.get(key);
        if (!e) return false;
        this.retained -= charge(e);
        return this.values.delete(key);
    }
    // Discard all entries and reset retained-byte accounting
    clear() {
        this.values.clear();
        this.retained = 0;
    }
    // Read an entry without changing its recency
    peek(key: string, now: number): T | undefined {
        if (!time(now)) return undefined;
        const e = this.values.get(key);
        if (e && e.expires && e.expires <= now) {
            this.delete(key);
            return undefined;
        }
        return e?.value;
    }
    // Read a live entry and update its access metadata
    get(key: string, now: number): T | undefined {
        const value = this.peek(key, now);
        const e = this.values.get(key);
        if (time(now) && e) {
            this.values.delete(key);
            this.values.set(key, e);
        }
        return value;
    }
    // Return the currently retained cache entries
    entries(now: number): Entry<T>[] {
        if (!time(now)) throw Error('Invalid cache time');
        for (const e of this.values.values()) if (e.expires && e.expires <= now) this.delete(e.key);
        return [...this.values.values()].map((e) => ({ ...e }));
    }
    // Evict entries until the configured limits are satisfied
    trim(reserved: number, now: number): string[] {
        if (!Number.isInteger(reserved) || reserved < 0 || reserved > this.limit || !time(now))
            throw Error('Invalid cache reservation');
        const expired = [...this.values.values()]
            .filter((e) => e.expires && e.expires <= now)
            .map((e) => e.key)
            .sort(compare);
        for (const key of expired) this.delete(key);
        for (const e of this.values.values()) {
            if (this.bytes + reserved <= this.limit && this.size <= this.capacity) break;
            this.delete(e.key);
            expired.push(e.key);
        }
        return expired;
    }
    // Insert a value while enforcing cache limits
    put(key: string, value: T, cost: number, expires: number, now: number): string[] {
        if (
            typeof key !== 'string' ||
            !key ||
            Buffer.from(key).toString() !== key ||
            bytes(key) > 4096 ||
            !Number.isInteger(cost) ||
            cost < 0 ||
            cost + bytes(key) + 64 > this.limit ||
            !time(expires) ||
            !time(now)
        )
            throw Error('Invalid or oversized cache entry');
        this.delete(key);
        const evicted = this.trim(0, now);
        if (expires && expires <= now) return evicted;
        const e = { key, value, cost, expires };
        this.values.set(key, e);
        this.retained += charge(e);
        return evicted.concat(this.trim(0, now));
    }
}
export class Budget {
    private retained = 0;
    // Initialize the shared retained-byte reservation limit
    constructor(readonly limit: number) {
        if (!Number.isInteger(limit) || limit < 1 || limit > MAX_BYTES)
            throw Error('Invalid byte budget');
    }
    // Report the currently reserved byte budget
    get used() {
        return this.retained;
    }
    // Reserve capacity before accepting additional work
    reserve(size: number): boolean {
        if (!Number.isInteger(size) || size < 0 || size > this.limit - this.retained) return false;
        this.retained += size;
        return true;
    }
    // Return a previously reserved byte charge
    release(size: number) {
        if (!Number.isInteger(size) || size < 0 || size > this.retained)
            throw Error('Invalid budget release');
        this.retained -= size;
    }
}
/** Explicit private host storage; receipt recovery, bounded batches, no fallback.
 * LRU order is session-local. Restore is atomic; flush may span several commits. */
export class Persistence<T> {
    private state: ManagedState;
    private ready = false;
    private busy = false;
    private pending?: Record<string, unknown>;
    // Bind cache serialization to its managed-state backend
    constructor(backend: Pick<HostStore, 'listKeys' | 'get' | 'receipt' | 'commit'>) {
        this.state = new ManagedState('private', backend);
    }
    // Serialize persistence operations without poisoning later requests
    private async exclusive(work: () => Promise<void>) {
        if (this.busy) throw Error('Cache persistence is busy');
        this.busy = true;
        try {
            await work();
        } finally {
            this.busy = false;
        }
    }
    // Load the persisted cache under its configured limits
    load(cache: Cache<T>, now: number, signal?: AbortSignal): Promise<void> {
        return this.exclusive(
            // Restore the persisted entries while holding the serialization slot
            async () => {
                if (this.ready || cache.capacity > 4096 || cache.limit > 8 << 20 || !time(now))
                    throw Error('Invalid persistent cache state or limits');
                const records = await this.state.load(signal),
                    temporary = new Cache<T>(cache.capacity, cache.limit);
                for (const key of Object.keys(records).sort(compare)) {
                    const e = records[key] as { value: T; cost: number; expires: number };
                    if (!e || !Object.hasOwn(e, 'value') || e.cost < bytes(encodeJSON(e.value)))
                        throw Error('Invalid persistent cache entry');
                    temporary.put(key, e.value, e.cost, e.expires, now);
                }
                cache.clear();
                for (const e of temporary.entries(now))
                    cache.put(e.key, e.value, e.cost, e.expires, now);
                this.ready = true;
            },
        );
    }
    // Write the current cache state to durable storage
    flush(cache: Cache<T>, now: number, signal?: AbortSignal): Promise<void> {
        return this.exclusive(
            // Persist a consistent cache snapshot under the serialization slot
            async () => {
                if (!this.ready || !time(now))
                    throw Error('Cache persistence is not loaded or time is invalid');
                const desired = Object.create(null) as Record<string, unknown>;
                for (const e of cache.entries(now)) {
                    if (e.cost < bytes(encodeJSON(e.value)))
                        throw Error('Invalid persistent cache cost');
                    desired[e.key] = { value: e.value, cost: e.cost, expires: e.expires };
                }
                // Take an immutable snapshot before yielding to the host
                const target = JSON.parse(encodeJSON(desired)) as Record<string, unknown>;
                for (const [identity, value] of Object.entries(target))
                    if (bytes(encodeJSON({ identity, value })) > 256 << 10)
                        throw Error('Persistent cache record exceeds 256 KiB');
                if (this.pending) {
                    await this.state.save(this.pending, signal);
                    this.pending = undefined;
                }
                for (let step = 0; step < 8193; step++) {
                    signal?.throwIfAborted();
                    const written = await this.state.load(signal);
                    const removed = Object.keys(written)
                        .filter((k) => !Object.hasOwn(target, k))
                        .sort(compare);
                    const changed = Object.keys(target)
                        .filter(
                            (k) =>
                                !Object.hasOwn(written, k) ||
                                !isDeepStrictEqual(target[k], written[k]),
                        )
                        .sort(compare);
                    const keys = removed.concat(changed);
                    if (!keys.length) return;
                    let batchBytes = 0;
                    for (const key of keys.slice(0, 32)) {
                        const recordBytes = Object.hasOwn(target, key)
                            ? bytes(encodeJSON({ identity: key, value: target[key] }))
                            : 0;
                        if (recordBytes > 256 << 10)
                            throw Error('Persistent cache record exceeds 256 KiB');
                        const size = recordBytes + 256;
                        if (batchBytes + size > 400 << 10) break;
                        batchBytes += size;
                        if (Object.hasOwn(target, key))
                            Object.defineProperty(written, key, {
                                value: target[key],
                                enumerable: true,
                                configurable: true,
                                writable: true,
                            });
                        else delete written[key];
                    }
                    this.pending = written;
                    await this.state.save(written, signal);
                    this.pending = undefined;
                }
                throw Error('Cache flush exceeded batch budget');
            },
        );
    }
}
