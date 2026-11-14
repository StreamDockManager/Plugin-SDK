import { content } from './inspector-assets.js';
// Resolve the shipped inspector asset paths
export function assets(): Record<string, Uint8Array> {
    return Object.fromEntries(
        Object.entries(content).map(([name, data]) => [name, new TextEncoder().encode(data)]),
    );
}
