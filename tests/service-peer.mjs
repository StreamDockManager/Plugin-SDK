// Disposable loopback peer shared by the four service-session suites
import { pathToFileURL } from 'node:url';
const { WebSocketServer } = await import(
    process.env.STREAM_DOCK_MANAGER_WS_MODULE
        ? pathToFileURL(process.env.STREAM_DOCK_MANAGER_WS_MODULE).href
        : new URL('../Node/node_modules/ws/wrapper.mjs', import.meta.url).href
);
import { createServer } from 'node:https';
import { readFileSync } from 'node:fs';
const secure = process.argv.includes('--tls');
const tlsRoot = process.env.STREAM_DOCK_MANAGER_TEST_TLS
    ? pathToFileURL(process.env.STREAM_DOCK_MANAGER_TEST_TLS.replace(/[\\/]?$/, '/'))
    : new URL('../contracts/fixtures/tls/', import.meta.url);
const http = secure
    ? createServer({
          cert: readFileSync(new URL('localhost-cert.pem', tlsRoot)),
          key: readFileSync(new URL('localhost-key.pem', tlsRoot)),
      })
    : undefined;
const server = new WebSocketServer(
    http ? { server: http } : { port: 0, host: '127.0.0.1', maxPayload: 4 << 20 },
);
// Create an isolated handshake and response queue for each test connection
server.on('connection', (socket, request) => {
    if (request.url === '/silent') return;
    socket.send(JSON.stringify({ hello: true }));
    let authed = false;
    const pair = [];
    // Simulate authentication, bounded messages, disconnects and reordered responses
    socket.on('message', (raw) => {
        const message = JSON.parse(raw.toString());
        if (!authed) {
            if (message.auth !== 'fixture') {
                socket.close();
                return;
            }
            authed = true;
            socket.send(JSON.stringify({ ready: true }));
            return;
        }
        if (message.id === 'large') {
            socket.send('x'.repeat((4 << 20) + 1));
            return;
        }
        if (message.id === 'close') {
            socket.terminate();
            return;
        }
        if (message.id === 'wait') {
            socket.send(JSON.stringify({ event: 'waiting' }));
            return;
        }
        // Reply in reverse order to exercise request correlation rather than arrival order
        if (message.id === 'one' || message.id === 'two') {
            pair.push(message);
            if (pair.length === 2) {
                socket.send(JSON.stringify(pair[1]));
                socket.send(JSON.stringify(pair[0]));
            }
            return;
        }
        socket.send(JSON.stringify(message));
    });
    // Keep intentional test disconnects from becoming unhandled socket errors
    socket.on('error', () => {});
});
// Publish the selected ephemeral loopback endpoint for the service tests
const ready = () =>
    console.log(`${secure ? 'wss' : 'ws'}://127.0.0.1:${(http ?? server).address().port}`);
if (http) http.listen(0, '127.0.0.1', ready);
else server.once('listening', ready);
// Terminate test clients and close the listener when the owning harness stops
const stop = () => {
    for (const socket of server.clients) socket.terminate();
    server.close(() => (http ? http.close(() => process.exit(0)) : process.exit(0)));
};
process.stdin.resume();
process.stdin.on('end', stop);
process.on('SIGTERM', stop);
