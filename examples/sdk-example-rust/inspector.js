const fields = [{ key: 'prefix', label: 'Label prefix', type: 'text', default: 'Count' }];
let current = {};
let connected = false;
// The transport and save handshake are shared and packaged locally
const inspector = window.StreamDockManagerInspector({
    // Disable editing until the inspector transport is connected
    connected(value) {
        connected = value;
        document.querySelectorAll('input, button').forEach((node) => {
            node.disabled = !value;
        });
    },
    // Render the latest settings without losing the current counter value
    render(settings) {
        current = structuredClone(settings);
        window.StreamDockManagerFields.render(document.querySelector('#fields'), fields, settings, {
            idPrefix: '',
        });
        document.querySelector('#prefix').maxLength = 80;
        document.querySelector('#prefix').disabled = !connected;
        document.querySelector('#status').textContent = `Counter: ${settings.count || 0}`;
    },
    // Collect editable fields while retaining settings not represented by controls
    collect() {
        return window.StreamDockManagerFields.collect(fields, current, { idPrefix: '' });
    },
    // Show transport and save feedback in the inspector status area
    status(text) {
        document.querySelector('#status').textContent = text;
    },
    // Refresh the displayed count when the plugin responds to inspector messages
    message(event) {
        if (event.event === 'sendToPropertyInspector' && event.payload?.settings)
            document.querySelector('#status').textContent =
                `Counter: ${event.payload.settings.count || 0}`;
    },
});
// Commit the current inspector draft through the shared save handshake
document.querySelector('#save').onclick = () => inspector.save();
// Restore the last saved inspector values
document.querySelector('#cancel').onclick = () => inspector.cancel();
// Ask the plugin to reset its counter rather than editing hidden state locally
document.querySelector('#reset').onclick = () => inspector.send({ reset: true });
