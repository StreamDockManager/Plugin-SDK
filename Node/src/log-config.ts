type Sink = (payload: Record<string, unknown>, failed: boolean) => void;
export class NetworkDiagnostics {
    #active?: { sink: Sink; redact: (text: string) => string };
    #sequence = 0;
    // Apply the host logging configuration
    configure(enabled: boolean, sink: Sink, redact: (text: string) => string): void {
        if (!enabled) this.#active = undefined;
        else if (!this.#active) this.#active = { sink, redact };
    }
    // Report whether diagnostic I/O logging is enabled
    io(operation: string, bytes: number): void {
        try {
            this.#active?.sink({ message: `Connection ${operation} · ${bytes} bytes` }, false);
        } catch {}
    }
    // Report whether network tracing is enabled
    trace<T>(
        create: (id: number, sink: Sink, redact: (text: string) => string) => T,
    ): T | undefined {
        if (!this.#active) return undefined;
        const owner = this.#active;
        return create(
            ++this.#sequence,
            (payload, failed) => {
                if (this.#active === owner) owner.sink(payload, failed);
            },
            owner.redact,
        );
    }
}
