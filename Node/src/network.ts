import { byteLength } from './internal/bytes.js';
import { NetworkDiagnostics } from './log-config.js';
export class NetworkPayload {
    readonly diagnostics = new NetworkDiagnostics();
    sent = 0;
    received = 0;
    // Add byte counts without exceeding the supported counter range
    add(sent: number, received: number): void {
        this.sent += sent;
        this.received += received;
    }
    // Wrap a stream with network accounting
    track(stream: PayloadStream): TrackedStream {
        return new TrackedStream(stream, this);
    }
    // Return a copy of current network counters
    snapshot(): { sent: number; received: number } {
        return { sent: this.sent, received: this.received };
    }
}
export interface PayloadStream {
    // Read from the underlying stream while accounting for bytes
    read(...args: any[]): any;
    // Write to the underlying stream while accounting for bytes
    write(value: any, ...args: any[]): any;
}
export class TrackedStream {
    // Associate byte accounting with the underlying stream
    constructor(
        readonly stream: PayloadStream,
        readonly network: NetworkPayload,
    ) {}
    // Read from the underlying stream while accounting for bytes
    read(...args: any[]): any {
        return this.account(this.stream.read(...args), 'read');
    }
    // Write to the underlying stream while accounting for bytes
    write(value: any, ...args: any[]): any {
        return this.account(this.stream.write(value, ...args), 'write', value);
    }
    // Apply a traffic sample to the current counters
    private account(result: any, direction: 'read' | 'write', input?: any): any {
        const read = direction === 'read';
        // Record bytes for the selected traffic direction
        const count = (value: any) => {
            const chunk = read ? value : input;
            const size =
                !read && typeof value === 'number'
                    ? value
                    : typeof chunk === 'string'
                      ? byteLength(chunk)
                      : (chunk?.byteLength ?? 0);
            this.network.add(read ? 0 : size, read ? size : 0);
            this.network.diagnostics.io(direction, size);
            return value;
        };
        return result instanceof Promise ? result.then(count) : count(result);
    }
}
