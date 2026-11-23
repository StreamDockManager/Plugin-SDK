import { type Event, UnsupportedFeatureError } from './protocol.js';
import type { Client } from './runtime.js';
import { control, privateError, RPCError } from './control.js';
import { byteLength } from './internal/bytes.js';

export const PERMISSIONS_PROTOCOL = 'plugin-permissions-v1';
export const HTTP_BROKER_PROTOCOL = 'plugin-http-broker-v1';
export const NOTIFICATIONS_PROTOCOL = 'plugin-notifications-v1';
export type NotificationLevel = 'info' | 'success' | 'warning' | 'error';
export type PermissionScope = Record<string, string>;
export interface PermissionStatus {
    granted: boolean;
    available: boolean;
    revision: string;
}
export interface PermissionList {
    revision: string;
    scopes: { action: string; scope: PermissionScope; granted: boolean; available: boolean }[];
}
export interface PermissionReview {
    review_id: string;
    revision: string;
    state: 'pending';
}
export interface RecipeResult {
    result_handle: string;
    state: 'pending';
}

export class Permissions {
    constructor(private readonly client: Client) {}
    // Send a plain-text notice under the reviewed action scope
    async createNotification(
        action: string,
        level: NotificationLevel,
        message: string,
        signal?: AbortSignal,
    ): Promise<void> {
        if (!this.client.supports(NOTIFICATIONS_PROTOCOL))
            throw new UnsupportedFeatureError(NOTIFICATIONS_PROTOCOL);
        if (
            typeof action !== 'string' ||
            !/^[A-Za-z0-9_.-]{1,256}$/.test(action) ||
            !['info', 'success', 'warning', 'error'].includes(level) ||
            typeof message !== 'string' ||
            !message.trim()
        ) {
            throw new RPCError('invalid_request', 'Invalid notification action, level or message');
        }
        if (/[\uD800-\uDFFF]/u.test(message))
            throw new RPCError('invalid_request', 'Invalid notification message');
        if (byteLength(message) > 512)
            throw new RPCError('too_large', 'Notification message exceeds 512 UTF-8 bytes');
        const receipt = await control(this.client)
            .request(
                PERMISSIONS_PROTOCOL,
                'create-notification',
                { action, level, message },
                signal,
            )
            .catch(privateError);
        if (
            !receipt ||
            typeof receipt !== 'object' ||
            Array.isArray(receipt) ||
            receipt.state !== 'created'
        )
            throw new RPCError('invalid_response', 'Invalid notification receipt');
    }
    getPermissions(signal?: AbortSignal): Promise<PermissionList> {
        return control(this.client)
            .request(PERMISSIONS_PROTOCOL, 'get-permissions', {}, signal)
            .catch(privateError);
    }
    hasPermission(
        action: string,
        scope: PermissionScope,
        signal?: AbortSignal,
    ): Promise<PermissionStatus> {
        return control(this.client)
            .request(PERMISSIONS_PROTOCOL, 'has-permission', { action, scope }, signal)
            .catch(privateError);
    }
    requestPermissions(
        activationToken: string,
        scopes: PermissionScope[],
        signal?: AbortSignal,
    ): Promise<PermissionReview> {
        return control(this.client)
            .request(
                PERMISSIONS_PROTOCOL,
                'request-permissions',
                { activation_token: activationToken, scopes },
                signal,
            )
            .catch(privateError);
    }
    sendExampleWebhook(
        activationToken: string,
        certificate: string,
        signal?: AbortSignal,
    ): Promise<RecipeResult> {
        return control(this.client)
            .request(
                HTTP_BROKER_PROTOCOL,
                'manager-webhook',
                { activation_token: activationToken, certificate },
                signal,
            )
            .catch(privateError);
    }
    sendLocalWebhook(
        activationToken: string,
        origin: string,
        signal?: AbortSignal,
    ): Promise<RecipeResult> {
        return control(this.client)
            .request(
                HTTP_BROKER_PROTOCOL,
                'manager-local-webhook',
                { activation_token: activationToken, origin },
                signal,
            )
            .catch(privateError);
    }

    summarizeClipboard(activationToken: string, signal?: AbortSignal): Promise<RecipeResult> {
        return control(this.client)
            .request(
                HTTP_BROKER_PROTOCOL,
                'clipboard-summary',
                { activation_token: activationToken },
                signal,
            )
            .catch(privateError);
    }

    readClipboardText(
        activationToken: string,
        signal?: AbortSignal,
    ): Promise<{ text: string; revision: string }> {
        return control(this.client)
            .request(
                PERMISSIONS_PROTOCOL,
                'read-clipboard-text',
                { activation_token: activationToken },
                signal,
            )
            .catch(privateError);
    }
    showTextResult(
        activationToken: string,
        text: string,
        signal?: AbortSignal,
    ): Promise<RecipeResult> {
        return control(this.client)
            .request(
                PERMISSIONS_PROTOCOL,
                'show-text-result',
                { activation_token: activationToken, text },
                signal,
            )
            .catch(privateError);
    }
    notifyCompletion(
        activationToken: string,
        signal?: AbortSignal,
    ): Promise<{ state: 'completed' }> {
        return control(this.client)
            .request(
                PERMISSIONS_PROTOCOL,
                'notify-completion',
                { activation_token: activationToken },
                signal,
            )
            .catch(privateError);
    }

    listVaultEntries(
        action: string,
        scope: PermissionScope,
        start = 0,
        signal?: AbortSignal,
    ): Promise<VaultEntryPage> {
        return control(this.client)
            .request(
                VAULT_REFERENCES_PROTOCOL,
                'list-vault-entries',
                { action, scope, start },
                signal,
            )
            .catch(privateError);
    }
    selectVaultEntry(
        activationToken: string,
        action: string,
        scope: PermissionScope,
        signal?: AbortSignal,
    ): Promise<PermissionReview> {
        return control(this.client)
            .request(
                VAULT_REFERENCES_PROTOCOL,
                'select-vault-entry',
                { action, scope, activation_token: activationToken },
                signal,
            )
            .catch(privateError);
    }
    async runVaultRecipe(
        activationToken: string,
        scope: PermissionScope,
        entryRef: string,
        signal?: AbortSignal,
    ): Promise<RecipeResult> {
        if (scope.recipe !== 'clipboard-summary')
            throw new RPCError('invalid_request', 'Unknown Vault recipe');
        return control(this.client)
            .request(
                HTTP_BROKER_PROTOCOL,
                scope.recipe,
                { scope, entry_ref: entryRef, activation_token: activationToken },
                signal,
            )
            .catch(privateError);
    }
}
export const VAULT_REFERENCES_PROTOCOL = 'plugin-vault-references-v1';
export interface VaultEntryPage {
    entries: { id: string; name: string }[];
    next: number | null;
}

export type PermissionNotice =
    | {
          event: 'streamDockManagerPermissionRequestResult';
          request_id: string;
          review_id: string;
          outcome: string;
          revision: string;
      }
    | {
          event: 'streamDockManagerPermissionsChanged';
          action: string;
          scopes: PermissionScope[];
          revision: string;
      }
    | {
          event: 'streamDockManagerVaultSelectionResult';
          request_id: string;
          review_id: string;
          outcome: string;
          entry?: { id: string; name: string };
      };

export function permissionNotice(event: Event): PermissionNotice | undefined {
    const wire = event as Event & Partial<Record<string, unknown>>;
    switch (wire.event) {
        case 'streamDockManagerPermissionRequestResult':
            if (
                typeof wire.request_id === 'string' &&
                typeof wire.review_id === 'string' &&
                typeof wire.outcome === 'string' &&
                typeof wire.revision === 'string'
            )
                return {
                    event: wire.event,
                    request_id: wire.request_id,
                    review_id: wire.review_id,
                    outcome: wire.outcome,
                    revision: wire.revision,
                };
            break;
        case 'streamDockManagerPermissionsChanged':
            if (
                typeof wire.action === 'string' &&
                typeof wire.revision === 'string' &&
                Array.isArray(wire.scopes)
            )
                return {
                    event: wire.event,
                    action: wire.action,
                    revision: wire.revision,
                    scopes: wire.scopes,
                };
            break;
        case 'streamDockManagerVaultSelectionResult': {
            if (
                typeof wire.request_id !== 'string' ||
                typeof wire.review_id !== 'string' ||
                typeof wire.outcome !== 'string'
            )
                break;
            const entry = wire.entry;
            const base = {
                event: wire.event,
                request_id: wire.request_id,
                review_id: wire.review_id,
                outcome: wire.outcome,
            } as const;
            if (entry === undefined) return base;
            if (
                entry &&
                typeof entry === 'object' &&
                'id' in entry &&
                typeof entry.id === 'string' &&
                'name' in entry &&
                typeof entry.name === 'string'
            )
                return { ...base, entry: { id: entry.id, name: entry.name } };
            break;
        }
    }
}
