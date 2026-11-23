import type { Client } from './runtime.js';
import { UnsupportedFeatureError } from './protocol.js';
import { control, privateError, RPCError } from './control.js';
import { byteLength } from './internal/bytes.js';

export const TEXT_REFERENCES_PROTOCOL = 'plugin-text-references-v1';

/** Resolve one transient string in a permission-checked Manager action scope */
export async function formatText(
    client: Client,
    action: string,
    text: string,
    signal?: AbortSignal,
): Promise<string> {
    if (typeof text !== 'string' || /[\uD800-\uDFFF]/u.test(text) || byteLength(text) > 64 << 10)
        throw new RPCError('invalid_request', 'Invalid text to format');
    const opener = text.indexOf('${');
    if (opener < 0 || text.indexOf('}', opener + 2) < 0) return text;
    if (!client.supports(TEXT_REFERENCES_PROTOCOL))
        throw new UnsupportedFeatureError(TEXT_REFERENCES_PROTOCOL);
    if (
        typeof action !== 'string' ||
        !action ||
        action.length > 256 ||
        /[^A-Za-z0-9_.-]/.test(action)
    )
        throw new RPCError('invalid_request', 'Invalid Vault reference');
    const result = await control(client)
        .request(TEXT_REFERENCES_PROTOCOL, 'format-text', { action, text }, signal)
        .catch(privateError);
    if (
        !result ||
        typeof result !== 'object' ||
        Array.isArray(result) ||
        Object.keys(result).join(',') !== 'text' ||
        typeof result.text !== 'string' ||
        /[\uD800-\uDFFF]/u.test(result.text) ||
        byteLength(result.text) > 256 << 10
    )
        throw new RPCError('invalid_response', 'Invalid formatted text response');
    return result.text;
}
