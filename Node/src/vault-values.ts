import type { Client } from './runtime.js';
import { UnsupportedFeatureError } from './protocol.js';
import { control, privateError, RPCError } from './control.js';
import { byteLength } from './internal/bytes.js';

export const VAULT_VALUES_PROTOCOL = 'plugin-vault-values-v1';
export const MAX_VAULT_TEXT_BYTES = 64 << 10;
export class VaultTextValue {
    readonly entry_kind = 'text' as const;
    constructor(
        readonly value: string,
        readonly revision: string,
    ) {}
    [Symbol.for('nodejs.util.inspect.custom')]() {
        return { entry_kind: 'text', value: '[redacted]', revision: this.revision };
    }
    toJSON() {
        return { entry_kind: 'text', value: '[redacted]', revision: this.revision };
    }
}
export class VaultLocation {
    constructor(
        readonly latitude: number,
        readonly longitude: number,
        readonly revision: string,
    ) {}
    [Symbol.for('nodejs.util.inspect.custom')]() {
        return '[Vault location]';
    }
    toJSON() {
        return '[Vault location]';
    }
}

function name(value: string, max: number): void {
    if (
        typeof value !== 'string' ||
        value.length < 1 ||
        value.length > max ||
        /[^A-Za-z0-9_.-]/.test(value)
    )
        throw new RPCError('invalid_request', 'Invalid Vault reference');
}
/** Read only host-authorized text references. No secret-sharing or local fallback. */
export class VaultValues {
    constructor(readonly client: Client) {
        if (!client.supports(VAULT_VALUES_PROTOCOL))
            throw new UnsupportedFeatureError(VAULT_VALUES_PROTOCOL);
    }
    async readLocation(
        slot: string,
        field: string,
        action: string,
        signal?: AbortSignal,
    ): Promise<VaultLocation> {
        name(slot, 128);
        name(field, 128);
        name(action, 256);
        const result = await control(this.client)
            .request(VAULT_VALUES_PROTOCOL, 'read-location', { slot, field, action }, signal)
            .catch(privateError);
        const coords = result?.value;
        if (
            !result ||
            Object.keys(result).sort().join(',') !== 'entry_kind,revision,value' ||
            result.entry_kind !== 'location' ||
            !(result.revision === '0' || positiveRevision(result.revision)) ||
            !coords ||
            Object.keys(coords).sort().join(',') !== 'latitude,longitude' ||
            !Number.isFinite(coords.latitude) ||
            Math.abs(coords.latitude) > 90 ||
            !Number.isFinite(coords.longitude) ||
            Math.abs(coords.longitude) > 180
        )
            throw new RPCError('invalid_response', 'Invalid Vault location response');
        return new VaultLocation(coords.latitude, coords.longitude, result.revision);
    }
    async readValue(
        slot: string,
        field: string,
        action: string,
        signal?: AbortSignal,
    ): Promise<VaultTextValue> {
        name(slot, 128);
        name(field, 128);
        name(action, 256);
        const result = await control(this.client)
            .request(VAULT_VALUES_PROTOCOL, 'read-value', { slot, field, action }, signal)
            .catch(privateError);
        if (
            !result ||
            typeof result !== 'object' ||
            Array.isArray(result) ||
            Object.keys(result).sort().join(',') !== 'entry_kind,revision,value' ||
            result.entry_kind !== 'text' ||
            typeof result.value !== 'string' ||
            /[\uD800-\uDFFF]/u.test(result.value) ||
            byteLength(result.value) > MAX_VAULT_TEXT_BYTES ||
            typeof result.revision !== 'string' ||
            !/^(0|[1-9][0-9]{0,19})$/.test(result.revision) ||
            BigInt(result.revision) > 18446744073709551615n ||
            BigInt(result.revision).toString() !== result.revision
        )
            throw new RPCError('invalid_response', 'Invalid Vault text response');
        return new VaultTextValue(result.value, result.revision);
    }
}

export const VAULT_SECRET_SHARING_PROTOCOL = 'plugin-vault-secret-sharing-v1';
export class VaultSecretValue {
    readonly entry_kind: 'api-key' | 'bearer-token' | 'credential';
    #value: string;
    constructor(
        value: string,
        readonly revision: string,
        kind: 'api-key' | 'bearer-token' | 'credential',
    ) {
        this.#value = value;
        this.entry_kind = kind;
    }
    /** Explicit disclosure to the caller; never place this value in logs/settings. */
    reveal(): string {
        return this.#value;
    }
    [Symbol.for('nodejs.util.inspect.custom')]() {
        return { entry_kind: this.entry_kind, value: '[redacted]', revision: this.revision };
    }
    toJSON() {
        return { entry_kind: this.entry_kind, value: '[redacted]', revision: this.revision };
    }
}
export class VaultSecrets {
    constructor(readonly client: Client) {
        if (!client.supports(VAULT_SECRET_SHARING_PROTOCOL))
            throw new UnsupportedFeatureError(VAULT_SECRET_SHARING_PROTOCOL);
    }
    async readSecret(
        slot: string,
        field: string,
        action: string,
        signal?: AbortSignal,
    ): Promise<VaultSecretValue> {
        name(slot, 128);
        name(field, 128);
        name(action, 256);
        const result = await control(this.client)
            .request(VAULT_SECRET_SHARING_PROTOCOL, 'read-secret', { slot, field, action }, signal)
            .catch(privateError);
        if (
            !result ||
            typeof result !== 'object' ||
            Array.isArray(result) ||
            Object.keys(result).sort().join(',') !== 'entry_kind,revision,value' ||
            !['api-key', 'bearer-token', 'credential'].includes(result.entry_kind) ||
            typeof result.value !== 'string' ||
            /[\uD800-\uDFFF]/u.test(result.value) ||
            byteLength(result.value) > MAX_VAULT_TEXT_BYTES ||
            typeof result.revision !== 'string' ||
            !/^(0|[1-9][0-9]{0,19})$/.test(result.revision) ||
            BigInt(result.revision) > 18446744073709551615n ||
            BigInt(result.revision).toString() !== result.revision
        )
            throw new RPCError('invalid_response', 'Invalid Vault secret response');
        return new VaultSecretValue(result.value, result.revision, result.entry_kind);
    }
}

export const VAULT_WRITES_PROTOCOL = 'plugin-vault-writes-v1';
export interface VaultWriteReceipt {
    entry_ref: string;
    revision: string;
}
const uuid = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}';
const entryRef = new RegExp('^' + uuid + '[.]' + uuid + '$');
function positiveRevision(s: unknown): s is string {
    return (
        typeof s === 'string' &&
        /^[1-9][0-9]{0,19}$/.test(s) &&
        BigInt(s) <= 18446744073709551615n &&
        String(BigInt(s)) === s
    );
}
export class VaultWrites {
    constructor(readonly client: Client) {
        if (!client.supports(VAULT_WRITES_PROTOCOL))
            throw new UnsupportedFeatureError(VAULT_WRITES_PROTOCOL);
    }
    async writeSecret(
        slot: string,
        field: string,
        action: string,
        kind: 'api-key' | 'credential' | 'bearer-token',
        value: string,
        expectedRevision?: string,
        signal?: AbortSignal,
    ): Promise<VaultWriteReceipt> {
        name(slot, 128);
        name(field, 128);
        name(action, 256);
        if (
            !['api-key', 'credential', 'bearer-token'].includes(kind) ||
            typeof value !== 'string' ||
            !value ||
            /[\uD800-\uDFFF]/u.test(value) ||
            byteLength(value) > MAX_VAULT_TEXT_BYTES ||
            (expectedRevision !== undefined && !positiveRevision(expectedRevision))
        )
            throw new RPCError('invalid_request', 'Invalid Vault write');
        const operation =
            expectedRevision === undefined
                ? { operation: 'create' }
                : { operation: 'rotate', expected_revision: expectedRevision };
        const receipt = await control(this.client)
            .request(
                VAULT_WRITES_PROTOCOL,
                'write-secret',
                { ...operation, slot, field, action, entry_kind: kind, value },
                signal,
            )
            .catch(privateError);
        if (
            !receipt ||
            typeof receipt !== 'object' ||
            Object.keys(receipt).sort().join(',') !== 'entry_ref,revision' ||
            typeof receipt.entry_ref !== 'string' ||
            !entryRef.test(receipt.entry_ref) ||
            !positiveRevision(receipt.revision)
        )
            throw new RPCError('invalid_response', 'Invalid Vault write receipt');
        return { entry_ref: receipt.entry_ref, revision: receipt.revision };
    }
}

export { formatText, TEXT_REFERENCES_PROTOCOL } from './text-references.js';
