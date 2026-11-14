import { createServer as tcp } from 'node:net';
import type { Socket } from 'node:net';
import { createServer as secure } from 'node:tls';
import { cert, key } from './test-certificates.js';
// Validate the configured peer TLS options
export function tls(): {
    server: { cert: string; key: string };
    client: { ca: string; rejectUnauthorized: true };
} {
    return { server: { cert, key }, client: { ca: cert, rejectUnauthorized: true } };
}
// Start the bounded peer server
export async function serve(
    handler: (socket: Socket) => void,
    encrypted = false,
): Promise<{
    port: number;
    // Stop peer admission and close owned connections
    close(): Promise<void>;
}> {
    const sockets = new Set<Socket>();
    // Admit an incoming peer connection
    const accept = (socket: Socket) => {
        sockets.add(socket);
        socket.setTimeout(2000, () => socket.destroy());
        socket.on('close', () => sockets.delete(socket));
        handler(socket);
    };
    const server = encrypted ? secure(tls().server, accept) : tcp(accept);
    await new Promise<void>((resolve, reject) => {
        server.once('error', reject);
        server.listen(0, '127.0.0.1', () => resolve());
    });
    const address = server.address();
    if (!address || typeof address === 'string') throw Error('Missing peer address');
    return {
        port: address.port,
        // Stop peer admission and close owned connections
        async close() {
            for (const socket of sockets) socket.destroy();
            await new Promise<void>((resolve, reject) =>
                server.close((error) => (error ? reject(error) : resolve())),
            );
        },
    };
}
