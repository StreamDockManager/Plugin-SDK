import { MAX_MESSAGE } from '../protocol.js';

export const MAX_INPUT_BYTES = 8 * MAX_MESSAGE;
export const MAX_INPUT_ITEMS = 128;

// Admission lives until dispatch finishes or its retained dial batch is retired
// WebSocket has no portable pause API; callers must close explicitly on overflow
export class IncomingBudget {
    bytes = 0;
    items = 0;
    // Reserve input-byte capacity before retaining an incoming frame
    acquire(size: number): () => void {
        if (!Number.isSafeInteger(size) || size < 0 || size > MAX_MESSAGE)
            throw Error('SDK incoming message exceeds limit');
        if (this.items >= MAX_INPUT_ITEMS || this.bytes + size > MAX_INPUT_BYTES)
            throw Error('SDK incoming queue full');
        this.items++;
        this.bytes += size;
        let released = false;
        return () => {
            if (released) return;
            released = true;
            this.items--;
            this.bytes -= size;
        };
    }
}
