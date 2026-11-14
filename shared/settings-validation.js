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
