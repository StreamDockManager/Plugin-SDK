/** Owner-serialized external operation history. Does not submit or retry work. */
export type State = 'pending' | 'confirmed' | 'failed' | 'uncertain';
export interface Record<T> {
    id: string;
    group: string;
    sequence: number;
    state: State;
    pinned: boolean;
    value: T;
    cost: number;
}
// Measure a string in UTF-8 bytes
const bytes = (s: string) => Buffer.byteLength(s);
// Include identifiers and record overhead in the retained cost
const charge = <T>(r: Record<T>) => bytes(r.id) + bytes(r.group) + r.cost + 64;
export class Ledger<T> {
    private values = new Map<string, Record<T>>();
    private retained = 0;
    private sequence = 0;
    // Validate the count and byte budgets for retained operation history
    constructor(
        readonly capacity: number,
        readonly limit: number,
    ) {
        if (
            !Number.isInteger(capacity) ||
            capacity < 1 ||
            capacity > 65536 ||
            !Number.isInteger(limit) ||
            limit < 1 ||
            limit > 64 << 20
        )
            throw Error('Invalid operation history limits');
    }
    // Report the number of retained operation records
    get size() {
        return this.values.size;
    }
    // Report the byte charge retained by operation history
    get bytes() {
        return this.retained;
    }
    // Return a copy of an operation record
    get(id: string): Record<T> | undefined {
        const r = this.values.get(id);
        return r ? { ...r } : undefined;
    }
    // Return operation records ordered by submission sequence
    records(group = ''): Record<T>[] {
        return [...this.values.values()]
            .filter((r) => !group || r.group === group)
            .sort((a, b) => a.sequence - b.sequence)
            .map((r) => ({ ...r }));
    }
    // Reserve a pending operation within count and byte budgets
    reserve(group: string, value: T, cost: number): Record<T> {
        if (
            typeof group !== 'string' ||
            !group ||
            bytes(group) > 4096 ||
            Buffer.from(group).toString() !== group ||
            !Number.isInteger(cost) ||
            cost < 0 ||
            cost > this.limit ||
            this.size >= this.capacity ||
            this.sequence >= Number.MAX_SAFE_INTEGER
        )
            throw Error('Invalid operation or history full');
        let sequence = this.sequence + 1,
            id = group;
        if (this.values.has(id)) {
            while (true) {
                id = group + '#' + sequence;
                if (!this.values.has(id)) break;
                if (++sequence > Number.MAX_SAFE_INTEGER)
                    throw Error('Operation sequence exhausted');
            }
        }
        const record: Record<T> = {
            id,
            group,
            sequence,
            state: 'pending',
            pinned: false,
            value,
            cost,
        };
        if (charge(record) > this.limit - this.retained)
            throw Error('Operation history byte budget exceeded');
        this.sequence = sequence;
        this.values.set(id, record);
        this.retained += charge(record);
        return { ...record };
    }
    // Replace an operation outcome while retaining budget accounting
    update(id: string, state: State, value: T, cost: number, pinned = false) {
        const prior = this.values.get(id);
        if (
            !prior ||
            !['pending', 'confirmed', 'failed', 'uncertain'].includes(state) ||
            !Number.isInteger(cost) ||
            cost < 0 ||
            cost > this.limit ||
            cost - prior.cost > this.limit - this.retained ||
            typeof pinned !== 'boolean'
        )
            throw Error('Invalid operation update or history full');
        this.values.set(id, { ...prior, state, value, cost, pinned });
        this.retained += cost - prior.cost;
    }
    // Discard completed unpinned records no longer used by active groups
    retire(active: ReadonlySet<string>): string[] {
        const out: string[] = [];
        for (const r of this.records())
            if (
                (r.state === 'confirmed' || r.state === 'failed') &&
                !r.pinned &&
                !active.has(r.group)
            ) {
                this.values.delete(r.id);
                this.retained -= charge(r);
                out.push(r.id);
            }
        return out;
    }
}
