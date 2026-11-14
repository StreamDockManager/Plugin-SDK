// SPDX-License-Identifier: MIT
// Shared inspector fields. This file is also embedded in every native SDK
// Keep field rendering state private while publishing the inspector helpers
(function () {
    const rendered = new WeakMap();
    // BEGIN GENERATED SETTINGS VALIDATION
    // SPDX-License-Identifier: MIT
    // Pure canonical settings validation; generated into Node and the inspector
    // Validate declared field types and defaults without discarding unknown settings
    function validateSettingsCore(fields, settings, browser, fail) {
        if (!settings || typeof settings !== 'object' || Array.isArray(settings)) fail('settings');
        if (fields.length > 256) fail('count');
        const output = structuredClone(settings),
            seen = new Set();
        // Reject invalid field declarations before reading or injecting their values
        for (const f of fields) {
            if (browser && f.sensitive && ['number', 'checkbox', 'pairs'].includes(f.type))
                fail('sensitive', f);
            if ((browser && typeof f.key !== 'string') || !f.key || seen.has(f.key)) fail('key', f);
            seen.add(f.key);
            if (
                f.path_picker &&
                (f.type !== 'path' || !['file', 'folder', 'save'].includes(f.path_picker))
            )
                fail('path', f);
            const isString = ['text', 'textarea', 'path', 'device', 'select'].includes(f.type);
            if (!isString && !['checkbox', 'number', 'pairs'].includes(f.type)) fail('type', f);
            if (f.sensitive && !isString) fail('sensitive', f);
            if (
                (f.min != null && !Number.isFinite(f.min)) ||
                (f.max != null && !Number.isFinite(f.max)) ||
                (f.min != null && f.max != null && f.min > f.max)
            )
                fail('bounds', f);
            // Define defaults as own data properties so special keys cannot mutate the prototype
            if (!Object.hasOwn(output, f.key)) {
                if (Object.hasOwn(f, 'default'))
                    Object.defineProperty(output, f.key, {
                        value: structuredClone(f.default),
                        enumerable: true,
                        writable: true,
                        configurable: true,
                    });
                else if (f.required) fail('required', f);
                else continue;
            }
            const v = output[f.key];
            let valid = false;
            if (isString)
                valid =
                    typeof v === 'string' &&
                    (!f.required || !!v) &&
                    (f.type !== 'select' || !!f.options?.includes(v));
            if (f.type === 'checkbox') valid = typeof v === 'boolean';
            if (f.type === 'number')
                valid =
                    Number.isFinite(v) &&
                    (f.min == null || v >= f.min) &&
                    (f.max == null || v <= f.max);
            if (f.type === 'pairs')
                valid =
                    !!v &&
                    typeof v === 'object' &&
                    !Array.isArray(v) &&
                    Object.values(v).every((x) => typeof x === 'string');
            if (!valid) fail('value', f);
        }
        return output;
    }

    // Diagnostic adapter keeps the two established public error contracts separate
    // Translate shared failure codes into the browser or SDK diagnostic contract
    function validateSettings(fields, settings, browser = false) {
        return validateSettingsCore(fields, settings, browser, (code, field = {}) => {
            const failure = { code, key: field.key, type: field.type };
            const common = {
                settings: 'Settings must be an object',
                count: 'Too many settings fields',
                key: 'Empty or duplicate settings field',
                value: `${failure.key}: invalid ${failure.type} value`,
            };
            const local = browser
                ? {
                      sensitive: `${failure.key}: sensitive requires a string field`,
                      path: `${failure.key}: invalid path picker`,
                      type: `${failure.key}: unknown field type ${failure.type}`,
                      bounds: `${failure.key}: invalid bounds`,
                      required: `${failure.key}: required`,
                  }
                : {
                      sensitive: 'Sensitive requires a string field',
                      path: 'Invalid path picker',
                      type: 'Unknown field type',
                      bounds: 'Invalid bounds',
                      required: 'Required field',
                  };
            throw Error(common[code] ?? local[code]);
        });
    }
    // END GENERATED SETTINGS VALIDATION
    // Apply the canonical validator using the browser diagnostic contract
    function validate(fields, settings) {
        return validateSettings(fields, settings, true);
    }
    // Collect visible controls while preserving unrelated draft keys
    function collect(fields, settings, options = {}) {
        const next = structuredClone(settings);
        for (const field of fields) {
            const node = document.getElementById(`${options.idPrefix ?? 'field-'}${field.key}`);
            if (field.type === 'pairs') {
                const pairs = {};
                for (const row of node.querySelectorAll('.pair')) {
                    const [key, value] = row.querySelectorAll('input');
                    if (key.value)
                        Object.defineProperty(pairs, key.value, {
                            value: value.value,
                            enumerable: true,
                            writable: true,
                            configurable: true,
                        });
                }
                next[field.key] = pairs;
            } else if (field.type === 'checkbox') next[field.key] = node.checked;
            else if (field.type === 'number') {
                if (!node.checkValidity()) throw Error(`Check ${field.label}.`);
                next[field.key] = node.valueAsNumber;
            } else next[field.key] = node.value;
        }
        return validate(fields, next);
    }
    // Update only changed rows and preserve active editing state within the same scope
    function render(container, fields, settings, options = {}) {
        const prior = rendered.get(container),
            scope = options.scope ?? '',
            state = { scope, rows: new Map() },
            active = container.contains(document.activeElement) ? document.activeElement : null,
            focus = active
                ? { id: active.id, start: active.selectionStart, end: active.selectionEnd }
                : null;
        if (focus && prior?.scope === scope) {
            for (const [key, row] of prior.rows) {
                if (row.label.contains(active)) {
                    focus.field = key;
                    focus.index = [
                        ...row.label.querySelectorAll('input, textarea, select, button'),
                    ].indexOf(active);
                    break;
                }
            }
        }
        const children = [];
        for (const field of fields) {
            const signature = JSON.stringify(field),
                valueKey = JSON.stringify(settings[field.key] ?? field.default ?? '');
            const old = prior?.scope === scope ? prior.rows.get(field.key) : null;
            // Existing callers replace the draft; editing flows opt into stable rows
            if (
                options.reset === false &&
                old?.signature === signature &&
                old.valueKey === valueKey
            ) {
                state.rows.set(field.key, old);
                children.push(old.label);
                continue;
            }
            const label = document.createElement(field.type === 'pairs' ? 'fieldset' : 'label');
            if (field.type === 'pairs') {
                const legend = document.createElement('legend');
                legend.textContent = field.label;
                label.append(legend);
            } else label.textContent = field.label;
            let node;
            const value = settings[field.key] ?? field.default ?? '';
            if (field.type === 'pairs') {
                node = document.createElement('div');
                const rows = document.createElement('div');
                // Append an editable key and value row with an independent remove control
                const add = (key = '', value = '') => {
                    const row = document.createElement('div');
                    row.className = 'pair';
                    const k = document.createElement('input'),
                        v = document.createElement('input'),
                        remove = document.createElement('button');
                    k.setAttribute('aria-label', field.label + ' name');
                    v.setAttribute('aria-label', field.label + ' value');
                    k.placeholder = 'Name';
                    k.value = key;
                    v.placeholder = 'Value';
                    v.value = value;
                    remove.type = 'button';
                    remove.textContent = 'Remove';
                    // Remove only the selected pair row from the unsaved draft
                    remove.onclick = () => row.remove();
                    row.append(k, v, remove);
                    rows.append(row);
                };
                for (const [k, v] of Object.entries(value || {})) add(k, String(v));
                const button = document.createElement('button');
                button.type = 'button';
                button.textContent = 'Add row';
                // Append an empty pair row without committing the settings
                button.onclick = () => add();
                node.append(rows, button);
            } else if (field.type === 'select' && !field.sensitive) {
                node = document.createElement('select');
                for (const option of field.options)
                    node.add(new Option(field.option_labels?.[option] ?? option, option));
                if (!field.options.includes(value)) node.add(new Option(`Stored: ${value}`, value));
                node.value = value;
            } else {
                node = document.createElement(
                    field.type === 'textarea' && !field.sensitive ? 'textarea' : 'input',
                );
                if (node.tagName === 'INPUT')
                    node.type = field.sensitive
                        ? 'password'
                        : field.type === 'checkbox'
                          ? 'checkbox'
                          : field.type === 'number'
                            ? 'number'
                            : 'text';
                if (field.sensitive) {
                    node.autocomplete = 'off';
                    node.spellcheck = false;
                }
                if (field.type === 'checkbox') {
                    node.checked = Boolean(value);
                    label.className = 'checkbox';
                } else node.value = value;
                if (field.type === 'number') {
                    node.required = true;
                    node.min = field.min;
                    node.max = field.max;
                    node.step = 'any';
                }
            }
            if (field.type === 'device') {
                const list = document.createElement('datalist');
                list.id = `options-${field.key}`;
                node.setAttribute('list', list.id);
                label.append(list);
            }
            node.id = `${options.idPrefix ?? 'field-'}${field.key}`;
            if (field.required) node.required = true;
            label.append(node);
            children.push(label);
            state.rows.set(field.key, { label, signature, valueKey });
        }
        // Move only changed/reordered rows. Unaffected inputs keep focus, selection
        // and uncommitted edits while unrelated settings/discovery updates arrive
        let cursor = container.firstChild;
        for (const child of children) {
            if (child === cursor) cursor = cursor.nextSibling;
            else container.insertBefore(child, cursor);
        }
        while (cursor) {
            const next = cursor.nextSibling;
            cursor.remove();
            cursor = next;
        }
        rendered.set(container, state);
        window.StreamDockManagerWorkflows?.restoreChoices(fields, options);
        if (focus && !active.isConnected && prior?.scope === scope) {
            const next = focus.id
                ? document.getElementById(focus.id)
                : state.rows
                      .get(focus.field)
                      ?.label.querySelectorAll('input, textarea, select, button')[focus.index];
            next?.focus();
            if (focus.start != null && typeof next?.setSelectionRange === 'function')
                next.setSelectionRange(focus.start, focus.end);
        }
    }
    window.StreamDockManagerFields = { validate, render, collect };
})();
