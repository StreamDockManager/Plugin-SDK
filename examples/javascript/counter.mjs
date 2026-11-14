import { Plugin } from 'stream-dock-manager-plugin-sdk/authoring';
import { PortableSettings } from 'stream-dock-manager-plugin-sdk/hoststorage';
// Normalize the counter fields while preserving unrelated saved settings
const clean = (settings = {}) => ({
    ...settings,
    prefix: typeof settings.prefix === 'string' ? settings.prefix.slice(0, 80) : 'Count',
    count: Number.isSafeInteger(settings.count) && settings.count >= 0 ? settings.count : 0,
});
// Create a counter plugin whose action IDs are registered when the host supplies them
export function counter() {
    const plugin = new Plugin();
    // Limit ordinary action logging to completed key presses
    plugin.shouldLogAction = (event) => event.event === 'keyUp';
    const handle = plugin.handle.bind(plugin);
    // Register per-action hooks and route inspector messages through the same counter state
    plugin.handle = async (client, event) => {
        if (event.action && !plugin.actions.has(event.action))
            plugin.register(event.action, {
                // Normalize the settings restored when a binding appears
                appeared(plugin, _client, event) {
                    const binding = plugin.bindings.get(event.context);
                    binding.settings = clean(binding.settings);
                },
                // Normalize settings supplied by a later host update
                changed(plugin, _client, event) {
                    const binding = plugin.bindings.get(event.context);
                    binding.settings = clean(binding.settings);
                },
                // Apply key presses or inspector edits and persist the resulting counter settings
                async handle(plugin, client, event) {
                    const binding = plugin.bindings.get(event.context);
                    if (!binding) return;
                    let settings = clean(binding.settings);
                    if (event.event === 'keyUp')
                        settings = {
                            ...settings,
                            count: (Number.isSafeInteger(settings.count) ? settings.count : 0) + 1,
                        };
                    else if (event.event === 'sendToPlugin' && event.payload?.reset)
                        settings = { ...settings, count: 0 };
                    else if (event.event === 'sendToPlugin' && event.payload?.settings)
                        settings = {
                            ...settings,
                            prefix:
                                typeof event.payload.settings.prefix === 'string'
                                    ? event.payload.settings.prefix.slice(0, 80)
                                    : 'Count',
                        };
                    else if (
                        event.event === 'sendToPlugin' &&
                        event.payload?.request === 'settings'
                    ) {
                        client.toInspector(event.context, { settings }, event.action);
                        return;
                    } else return;
                    binding.settings = settings;
                    await new PortableSettings(client, 'action', event.context).replace(settings);
                    client.toInspector(event.context, { settings }, event.action);
                },
                // Render the count consistently as a title, state, inline image and layout feedback
                render(_plugin, _context, binding) {
                    const prefix =
                        typeof binding.settings.prefix === 'string'
                            ? binding.settings.prefix.slice(0, 80)
                            : 'Count';
                    const count =
                        Number.isSafeInteger(binding.settings.count) && binding.settings.count >= 0
                            ? binding.settings.count
                            : 0;
                    const state = count % 2;
                    const path = state ? 'M16 6v20M6 16h20' : 'M7 9h18M7 19h18';
                    const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><g fill="none" stroke="#ffffff" stroke-width="3" stroke-linecap="round"><path d="${path}"/></g></svg>`;
                    const image = `data:image/svg+xml;base64,${Buffer.from(svg, 'utf8').toString('base64')}`;
                    return {
                        image,
                        title: `${prefix} ${count}`,
                        state,
                        layout: 'display/counter.json',
                        feedback: { icon: `icons/${state ? 'odd' : 'even'}.svg`, prefix, count },
                    };
                },
            });
        if (event.event === 'sendToPlugin') {
            const action = plugin.actions.get(event.action);
            await action?.handle?.(plugin, client, event);
            plugin.render(client, event.context);
            return;
        }
        await handle(client, event);
    };
    return plugin;
}
