import { Client } from './runtime.js';
import type { Handler } from './runtime.js';
import { HostInfo } from './protocol.js';
import type { Event } from './protocol.js';
export class Clock {
    // Initialize the deterministic clock at the requested instant
    constructor(private value = 0) {}
    now = (): number => this.value;
    // Advance the deterministic test clock
    advance(ms: number): void {
        if (!Number.isFinite(ms) || ms < 0) throw Error('Clock cannot go backwards');
        this.value += ms;
    }
}
export class Recorder {
    readonly client: Client;
    readonly history: Event[] = [];
    // Create an in-memory client for recording handler output
    constructor(host = new HostInfo()) {
        this.client = new Client(host, 'test-token');
    }
    // Collect messages emitted by the test client
    drain(): Event[] {
        const values = this.client.drain();
        this.history.push(...values);
        return values;
    }
    // Deliver fixture events through the handler
    async replay(handler: Handler, events: Event[]): Promise<void> {
        for (const event of events) await this.client.dispatch(handler, event);
    }
    // Run handler polling at the test clock time
    async poll(handler: Handler): Promise<void> {
        await this.client.poll(handler);
    }
    // Assert that emitted messages match the expected fixture
    expect(expected: Event[]): void {
        const value = this.drain();
        if (JSON.stringify(value) !== JSON.stringify(expected))
            throw Error('Recorded events differ');
    }
    // Verify messages refer only to the permitted contexts
    assertContexts(...allowed: string[]): void {
        if (this.history.some((x) => !allowed.includes(x.context ?? '')))
            throw Error('Event escaped allowed contexts');
    }
    // Retire the test handler and its pending work
    close(): void {
        this.client.close();
    }
}
export { serve, tls } from './peers.js';
