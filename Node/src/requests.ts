/** Bounded shared reads. Each subscriber owns its cancellation; no write replay. */
import { encodeJSON } from './internal/json.js';
interface Flight<T> {
    controller: AbortController;
    users: number;
    finished: boolean;
    retired: boolean;
    failed: boolean;
    value?: T;
    error?: unknown;
    waiters: Set<() => void>;
    timer: ReturnType<typeof setTimeout>;
}
export class RequestGroup<T = unknown> {
    private flights = new Map<string, Flight<T>>();
    private cooldowns = new Map<string, { until: number; error: unknown }>();
    private slots = 0;
    private closed = false;
    // Share one in-flight read among compatible callers
    async get(
        key: string,
        work: (signal: AbortSignal) => Promise<T>,
        signal?: AbortSignal,
        retryAfter: (error: unknown) => number = () => 0,
    ): Promise<T> {
        if (signal?.aborted) throw signal.reason ?? Error('Shared read cancelled');
        if (this.closed) throw Error('Shared read group closed');
        if (!key || Buffer.byteLength(key) > 2048) throw Error('Invalid shared read key');
        const now = performance.now();
        for (const [id, entry] of this.cooldowns) if (entry.until <= now) this.cooldowns.delete(id);
        const cooldown = this.cooldowns.get(key);
        if (cooldown) throw cooldown.error;
        let flight = this.flights.get(key);
        if (!flight) {
            if (this.slots + this.cooldowns.size >= 32)
                throw Error('Shared read capacity exhausted');
            const controller = new AbortController();
            flight = {
                controller,
                users: 0,
                finished: false,
                retired: false,
                failed: false,
                waiters: new Set(),
                timer: setTimeout(() => controller.abort(Error('Shared read timed out')), 8000),
            };
            const owner = flight;
            this.slots++;
            this.flights.set(key, owner);
            void Promise.resolve()
                .then(() => work(controller.signal))
                .then((value) => {
                    const data = encodeJSON(value);
                    if (data === undefined || Buffer.byteLength(data) > 1 << 20)
                        throw Error('Shared read exceeds 1 MiB');
                    owner.value = JSON.parse(data) as T;
                })
                .catch(
                    // Propagate the shared read failure to its remaining subscribers
                    (error) => {
                        owner.failed = true;
                        owner.error = error;
                        if (
                            this.flights.get(key) === owner &&
                            !this.closed &&
                            !controller.signal.aborted
                        ) {
                            let delay = 0;
                            try {
                                delay = retryAfter(error);
                            } catch {
                                /* no cooldown */
                            }
                            if (Number.isFinite(delay) && delay > 0)
                                this.cooldowns.set(key, {
                                    until: performance.now() + Math.min(delay, 9223372036854),
                                    error,
                                });
                        }
                    },
                )
                .finally(() => {
                    owner.finished = true;
                    clearTimeout(owner.timer);
                    if (owner.users === 0) this.slots--;
                    for (const notify of [...owner.waiters]) notify();
                });
        }
        const owner = flight;
        owner.users++;
        try {
            return await new Promise<T>((resolve, reject) => {
                // Settle the shared read and release its subscribers
                const finish = () => {
                    if (!owner.finished && !signal?.aborted && !owner.controller.signal.aborted)
                        return;
                    signal?.removeEventListener('abort', finish);
                    owner.controller.signal.removeEventListener('abort', finish);
                    owner.waiters.delete(finish);
                    if (
                        signal?.aborted ||
                        owner.controller.signal.aborted ||
                        owner.retired ||
                        this.closed
                    )
                        reject(
                            signal?.reason ??
                                owner.controller.signal.reason ??
                                Error('Shared read cancelled'),
                        );
                    else if (owner.failed) reject(owner.error);
                    else resolve(structuredClone(owner.value!));
                };
                signal?.addEventListener('abort', finish, { once: true });
                owner.controller.signal.addEventListener('abort', finish, { once: true });
                owner.waiters.add(finish);
                finish();
            });
        } finally {
            owner.users--;
            if (owner.users === 0) {
                if (this.flights.get(key) === owner) this.flights.delete(key);
                owner.controller.abort();
                clearTimeout(owner.timer);
                if (owner.finished) this.slots--;
            }
        }
    }
    // Cancel a pending read when no subscriber still needs it
    cancelPending() {
        for (const flight of this.flights.values()) {
            flight.retired = true;
            flight.controller.abort(Error('Shared read cancelled'));
            clearTimeout(flight.timer);
        }
        this.flights.clear();
    }
    // Cancel every shared read and stop new admission
    close() {
        this.closed = true;
        this.cancelPending();
        this.cooldowns.clear();
    }
}
