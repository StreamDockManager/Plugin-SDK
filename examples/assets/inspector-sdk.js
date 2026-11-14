// SPDX-License-Identifier: MIT
// Local property-inspector transport. No first-party host permissions are used
// Create a host-neutral inspector session with explicit save and cancel state
window.StreamDockManagerInspector = function (options) {
    let socket,
        context,
        identity,
        action,
        host,
        settings = {},
        globals = {},
        initialGlobals = {},
        globalsDirty = false,
        pendingSaves = 0,
        initial = {},
        generation = 0,
        globalScope = Boolean(options.global),
        managed = false;
    // Measure UTF-8 payload size without allocating another full encoded message
    const envelopeFits = (text) => {
        const limit = 1024 * 1024;
        if (typeof text !== 'string' || text.length > limit) return false;
        let bytes = 0;
        for (let index = 0; index < text.length; index++) {
            const value = text.charCodeAt(index);
            if (value < 128) bytes++;
            else if (value < 2048) bytes += 2;
            else if (
                value >= 0xd800 &&
                value <= 0xdbff &&
                text.charCodeAt(index + 1) >= 0xdc00 &&
                text.charCodeAt(index + 1) <= 0xdfff
            ) {
                bytes += 4;
                index++;
            } else bytes += 3;
            if (bytes > limit) return false;
        }
        return true;
    };
    // Forward transport and validation feedback to the inspector view
    const status = (text) => options.status?.(text);
    // Use the host-specific context when sending an inspector protocol envelope
    const send = (event, payload) => {
        if (socket?.readyState !== WebSocket.OPEN) throw Error('Settings connection unavailable');
        socket.send(
            JSON.stringify({
                event,
                context: host.streamDockManager ? context : identity,
                action,
                payload,
            }),
        );
    };
    // Apply only validated host palette colors and the resolved color scheme
    const theme = (value) => {
        if (!value) return;
        document.documentElement.style.colorScheme = value.resolved === 'light' ? 'light' : 'dark';
        for (const [key, color] of Object.entries(value.palette || {})) {
            if (/^[a-z-]+$/.test(key) && /^#[0-9a-f]{3,8}$/i.test(color))
                document.documentElement.style.setProperty(`--${key}`, color);
        }
    };
    // Invalidate old callbacks before releasing the current host socket
    const disconnect = () => {
        generation++;
        options.connected?.(false);
        if (socket) {
            // Register the inspector and request settings only for the current connection
            socket.onopen = socket.onmessage = socket.onclose = socket.onerror = null;
            socket.close();
            socket = undefined;
        }
    };
    const api = {
        // Send an inspector workflow request and report transport failures locally
        send(payload) {
            try {
                send('sendToPlugin', payload);
                return true;
            } catch (error) {
                status(error.message);
                return false;
            }
        },
        // Commit collected action and global drafts using the host save handshake
        save() {
            try {
                settings = { ...settings, ...options.collect?.() };
                pendingSaves = !globalScope && globalsDirty ? 2 : 1;
                if (!globalScope && globalsDirty) send('setGlobalSettings', globals);
                send(globalScope ? 'setGlobalSettings' : 'setSettings', settings);
                initial = structuredClone(settings);
                initialGlobals = structuredClone(globals);
                globalsDirty = false;
                if (!managed) status('Saved');
            } catch (error) {
                status(error.message);
            }
        },
        // Restore the last saved drafts without sending changes to the host
        cancel() {
            settings = structuredClone(initial);
            globals = structuredClone(initialGlobals);
            globalsDirty = false;
            options.globals?.(structuredClone(globals));
            options.render?.(structuredClone(settings));
            status('Changes discarded');
        },
        // Replace the action draft without committing it
        replaceSettings(value) {
            settings = structuredClone(value);
        },
        // Return a detached copy of the current global draft
        globalSettings() {
            return structuredClone(globals);
        },
        // Mark the replaced global draft for the next explicit save
        replaceGlobalSettings(value) {
            globals = structuredClone(value);
            globalsDirty = true;
        },
        // Expose connection metadata without sharing mutable settings drafts
        state() {
            return { context, action, host, globalScope, managed };
        },
        disconnect,
    };
    options.connected?.(false);
    // Connect the supplied loopback host and discard callbacks from older sessions
    window.connectOpenActionSocket =
        window.connectElgatoStreamDeckSocket =
        window.connectSocket =
            async function (port, uuid, register, info, actionInfo) {
                disconnect();
                const current = generation;
                try {
                    if (
                        !Number.isInteger(Number(port)) ||
                        Number(port) < 1 ||
                        Number(port) > 65535 ||
                        register !== 'registerPropertyInspector'
                    )
                        throw Error('Invalid inspector host arguments');
                    const address =
                        typeof actionInfo === 'string' ? JSON.parse(actionInfo) : actionInfo;
                    host = typeof info === 'string' ? JSON.parse(info) : info;
                    context = address.context;
                    globalScope = options.global ?? address.payload?.scope === 'plugin';
                    managed = Boolean(
                        host.streamDockManager && context.startsWith('settings-draft:'),
                    );
                    identity = uuid;
                    action = address.action;
                    settings = structuredClone(address.payload?.settings || {});
                    initial = structuredClone(settings);
                    globals = {};
                    initialGlobals = {};
                    globalsDirty = false;
                    pendingSaves = 0;
                    const initialized = await options.initialize?.(
                        structuredClone(settings),
                        host,
                        address,
                    );
                    if (current !== generation) return;
                    options.initialized?.(initialized);
                    options.render?.(structuredClone(settings));
                    theme(host.streamDockManager?.theme);
                    socket = new WebSocket(`ws://127.0.0.1:${Number(port)}`);
                    socket.onopen = () => {
                        if (current !== generation) return;
                        socket.send(JSON.stringify({ event: register, uuid }));
                        send(globalScope ? 'getGlobalSettings' : 'getSettings');
                        if (!globalScope) send('getGlobalSettings');
                        options.connected?.(true);
                        options.ready?.(api);
                    };
                    // Validate incoming envelopes before updating drafts and save acknowledgments
                    socket.onmessage = ({ data }) => {
                        if (current !== generation) return;
                        try {
                            if (!envelopeFits(data)) {
                                status('Settings response exceeds the 1 MiB UTF-8 limit');
                                return;
                            }
                            const event = JSON.parse(data);
                            if (event.context && ![context, identity].includes(event.context))
                                return;
                            if (event.event === 'didReceiveTheme') theme(event.payload);
                            if (
                                event.event === 'didReceiveGlobalSettings' &&
                                event.payload?.settings &&
                                !globalsDirty
                            ) {
                                globals = structuredClone(event.payload.settings);
                                initialGlobals = structuredClone(globals);
                                options.globals?.(structuredClone(globals));
                            }
                            if (event.event === 'settingsDraftSaved') {
                                if (pendingSaves > 0 && --pendingSaves > 0) return;
                                status('Saved');
                                window.parent.postMessage(
                                    { event: 'stream-dock-manager-settings-saved', context },
                                    '*',
                                );
                            }
                            if (
                                event.event ===
                                    (globalScope
                                        ? 'didReceiveGlobalSettings'
                                        : 'didReceiveSettings') &&
                                event.payload?.settings
                            ) {
                                settings = structuredClone(event.payload.settings);
                                initial = structuredClone(settings);
                                options.render?.(structuredClone(settings));
                            }
                            options.message?.(event);
                        } catch (error) {
                            status(
                                typeof error?.message === 'string'
                                    ? error.message.slice(0, 256)
                                    : 'Invalid settings response',
                            );
                        }
                    };
                    // Disable inspector controls when the active host connection closes
                    socket.onclose = () => {
                        options.connected?.(false);
                        status('Host disconnected');
                    };
                    // Report transport failure only for the active connection generation
                    socket.onerror = () => {
                        options.connected?.(false);
                        status('Settings connection unavailable');
                    };
                } catch (error) {
                    status(error.message);
                }
            };
    // Accept only the expected parent theme message for embedded inspector styling
    window.addEventListener('message', (event) => {
        if (
            event.source === window.parent &&
            event.data?.event === 'stream-dock-manager-save-settings'
        )
            api.save();
    });
    window.addEventListener('pagehide', disconnect);
    return api;
};
