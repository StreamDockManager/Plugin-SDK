// SPDX-License-Identifier: MIT
// Optional workflow helpers; service-specific consent and search remain in the plugin
// Keep discovery caches private while publishing reusable inspector workflows
(function () {
    const streamDockDiscovery = new WeakMap();
    const MAX_DISCOVERY_BYTES = 8 * 1024 * 1024;
    const MAX_DISCOVERY_ITEMS = 8192;
    // Estimate the retained memory of a compact discovery scope
    const charge = (scope, devices) =>
        64 +
        2 * scope.length +
        devices.reduce(
            (sum, device) =>
                sum + 128 + 2 * (device.id.length + device.name.length + device.kind.length),
            0,
        );
    // Sum all retained scope charges for the global discovery budget
    const retainedBytes = (scopes) =>
        [...scopes.values()].reduce((sum, entry) => sum + entry.bytes, 0);
    // ID and timeout ownership only. Payloads, consent and UI transitions stay local
    class RequestLifecycle {
        // Initialize request identity and generation tracking without scheduling a timer
        constructor() {
            this.pending = undefined;
            this.generation = 0;
        }
        // Replace the active request and schedule a generation-checked expiry callback
        start(id, timeout, expired) {
            this.finish();
            this.pending = id;
            const generation = this.generation;
            // Ignore stale timeouts after a request is replaced or explicitly completed
            this.timer = setTimeout(() => {
                if (generation !== this.generation || this.pending !== id) return;
                this.finish();
                expired();
            }, timeout);
        }
        // Accept only the currently pending request identifier
        accepts(id) {
            return this.pending !== undefined && this.pending === id;
        }
        // Cancel the timer and invalidate callbacks from the completed generation
        finish() {
            clearTimeout(this.timer);
            this.timer = undefined;
            this.pending = undefined;
            this.generation++;
        }
    }
    // One request per picker. Results never change the saved selection or discard
    // last-good choices on failure; committing a selection remains the caller's job
    let nextDiscovery = 0;
    class DiscoveryController {
        // Validate the discovery transport and initialize a picker with no active request
        constructor({ send, changed = () => {}, timeout = 10000, cancelOp } = {}) {
            if (
                typeof send !== 'function' ||
                !Number.isFinite(timeout) ||
                timeout <= 0 ||
                timeout > 120000
            )
                throw Error('Invalid discovery controller options');
            this.send = send;
            this.changed = changed;
            this.timeout = timeout;
            this.cancelOp = cancelOp;
            this.lifecycle = new RequestLifecycle();
            this.closed = false;
            this.phase = 'idle';
            this.choices = [];
        }
        // Expose the request identifier currently owned by this picker
        get pending() {
            return this.lifecycle.pending;
        }
        // Report whether discovery is awaiting a matching response
        get busy() {
            return this.pending !== undefined;
        }
        // Cancel earlier discovery and send a newly correlated request
        start(payload) {
            if (this.closed) throw Error('Discovery controller is closed');
            this.cancel();
            const id = String(++nextDiscovery);
            // Notify cancellation and timeout while retiring the expired remote request
            this.lifecycle.start(id, this.timeout, () => {
                this.phase = 'cancelled';
                this.changed(this.phase);
                if (this.cancelOp) {
                    try {
                        this.send({ op: this.cancelOp, request_id: id });
                    } catch {
                        /* locally retired */
                    }
                }
                this.phase = 'timeout';
                this.changed(this.phase);
            });
            this.phase = 'loading';
            this.changed(this.phase);
            try {
                if (this.send({ ...payload, request_id: id }) === false)
                    throw Error('Discovery transport unavailable');
            } catch (error) {
                if (this.pending === id) {
                    this.finish('error');
                    return undefined;
                }
                throw error;
            }
            return id;
        }
        // Reject discovery responses that do not match the active request
        accepts(payload) {
            return this.busy && payload?.request_id === this.pending;
        }
        // Validate matching results before replacing the last successful choices
        receive(payload, choices) {
            if (!this.accepts(payload)) return false;
            // Validation is atomic: invalid results leave the last-good list intact
            if (choices !== undefined && payload.available !== false && !payload.error) {
                if (
                    !Array.isArray(choices) ||
                    choices.length > MAX_DISCOVERY_ITEMS ||
                    new TextEncoder().encode(JSON.stringify(choices)).length > MAX_DISCOVERY_BYTES
                ) {
                    this.finish('error');
                    throw Error('Discovery result exceeds supported limits');
                }
                this.choices = structuredClone(choices);
            }
            this.finish(payload.available === false || payload.error ? 'error' : 'ready');
            return true;
        }
        // Retire the request and notify the view of its final phase
        finish(phase) {
            this.lifecycle.finish();
            this.phase = phase;
            this.changed(phase);
        }
        // Retire local discovery before asking the plugin to cancel remote work
        cancel() {
            const id = this.pending;
            if (id === undefined) return;
            this.finish('cancelled');
            if (this.cancelOp) {
                try {
                    this.send({ op: this.cancelOp, request_id: id });
                } catch {
                    /* locally retired */
                }
            }
        }
        // Cancel pending work and prevent the disposed picker from restarting
        close() {
            this.cancel();
            this.closed = true;
        }
        // Retain a saved selection even when discovery no longer returns it
        selection(id, label = `Saved selection ${id}`) {
            const choices = structuredClone(this.choices);
            if (id && !choices.some((choice) => String(choice.id) === String(id)))
                choices.unshift({ id, name: label });
            return choices;
        }
    }
    window.StreamDockManagerWorkflows = {
        DiscoveryController,
        RequestLifecycle,
        unchangedSecret: '[unchanged sensitive value]',
        // Serialize advanced settings with sensitive values replaced by placeholders
        advanced(settings, fields) {
            const value = structuredClone(settings);
            for (const field of fields)
                if (field.sensitive && Object.hasOwn(value, field.key))
                    value[field.key] = this.unchangedSecret;
            return JSON.stringify(value, null, 2);
        },
        // Restore unchanged secrets and validate edited advanced settings
        applyJSON(text, fields, current) {
            const value = JSON.parse(text);
            if (!value || typeof value !== 'object' || Array.isArray(value))
                throw Error('Settings must be an object');
            for (const field of fields)
                if (field.sensitive && value[field.key] === this.unchangedSecret)
                    value[field.key] = current[field.key];
            return window.StreamDockManagerFields.validate(fields, value);
        },
        // Validate and retain compact discovery choices within item and byte budgets
        choices(fields, devices, options = {}) {
            devices ??= [];
            const scope = options.scope ?? '';
            if (
                typeof scope !== 'string' ||
                !Array.isArray(devices) ||
                devices.length > MAX_DISCOVERY_ITEMS
            )
                throw Error('Discovery result exceeds supported limits');
            // Retain only fields used by the datalists, not each service's full
            // response objects. Validate before changing the last-good scope
            // Copy only scalar display fields rather than retaining complete service responses
            const compact = devices.map((device) => {
                // Accept text and finite numbers as stable discovery identifiers and labels
                const scalar = (value) =>
                    typeof value === 'string' ||
                    (typeof value === 'number' && Number.isFinite(value));
                if (
                    !device ||
                    !scalar(device.id) ||
                    !scalar(device.name) ||
                    (device.kind !== undefined && typeof device.kind !== 'string')
                )
                    throw Error('Invalid discovery choice');
                return {
                    id: String(device.id),
                    name: String(device.name),
                    kind: device.kind ?? '',
                };
            });
            const bytes = charge(scope, compact);
            if (bytes > MAX_DISCOVERY_BYTES)
                throw Error('Discovery result exceeds the 8 MiB cache limit');
            let scopes = streamDockDiscovery.get(fields);
            if (!scopes) streamDockDiscovery.set(fields, (scopes = new Map()));
            scopes.delete(scope);
            while (scopes.size >= 32 || retainedBytes(scopes) + bytes > MAX_DISCOVERY_BYTES)
                scopes.delete(scopes.keys().next().value);
            scopes.set(scope, { devices: compact, bytes });
            this.restoreChoices(fields, options);
        },
        // Clear the selected discovery scope without affecting other field groups
        clearChoices(fields, options = {}) {
            streamDockDiscovery.get(fields)?.delete(options.scope ?? '');
        },
        // Rebuild device datalists from cached choices for the current scope
        restoreChoices(fields, options = {}) {
            const scopes = streamDockDiscovery.get(fields);
            const scope = options.scope ?? '';
            const entry = scopes?.get(scope);
            if (entry) {
                scopes.delete(scope);
                scopes.set(scope, entry);
            }
            const devices = entry?.devices ?? [];
            for (const field of fields.filter((field) => field.type === 'device')) {
                const list = document.getElementById(`options-${field.key}`);
                if (!list) continue;
                list.replaceChildren();
                for (const device of devices) {
                    if (device.kind && device.kind !== field.key) continue;
                    const option = document.createElement('option');
                    option.value = String(device.id);
                    option.label = String(device.name);
                    list.append(option);
                }
            }
        },
        // Report retained scope, item and byte counts without exposing discovery contents
        discoveryUsage(fields) {
            const scopes = streamDockDiscovery.get(fields) ?? new Map();
            return {
                scopes: scopes.size,
                bytes: retainedBytes(scopes),
                items: [...scopes.values()].reduce((sum, entry) => sum + entry.devices.length, 0),
            };
        },
        // Allow only supported workflow operations through the inspector transport
        request(inspector, op, settings, scope) {
            if (!['discover', 'test', 'authorize', 'disconnect'].includes(op))
                throw Error('Unsupported inspector workflow');
            return inspector.send({ op, settings, scope });
        },
    };
})();
