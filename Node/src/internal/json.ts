/** Replacer and reviver preserving ordinary JSON semantics except invalid numbers. */
export function finiteJSONValue(message = 'Non-finite JSON number') {
    return (_key: string, item: any): any => {
        if (typeof item === 'number' && !Number.isFinite(item)) throw Error(message);
        return item;
    };
}
const strictNumber = finiteJSONValue('Expected a JSON value');

/** Preserve JSON semantics across language SDKs; never silently drop invalid values. */
export function encodeJSON(value: unknown): string {
    return JSON.stringify(
        value,
        // Reject non-finite JSON values before they cross the protocol boundary
        (_key, item) => {
            if (
                item === undefined ||
                typeof item === 'function' ||
                typeof item === 'symbol' ||
                typeof item === 'bigint'
            )
                throw Error('Expected a JSON value');
            return strictNumber(_key, item);
        },
    );
}
