package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const PermissionsProtocol = "plugin-permissions-v1"
const HTTPBrokerProtocol = "plugin-http-broker-v1"

type PermissionStatus struct {
	Granted   bool   `json:"granted"`
	Available bool   `json:"available"`
	Revision  string `json:"revision"`
}

type ActionPermissionStatus struct {
	Action    string            `json:"action"`
	Scope     map[string]string `json:"scope"`
	Granted   bool              `json:"granted"`
	Available bool              `json:"available"`
}

type PermissionList struct {
	Revision string                   `json:"revision"`
	Scopes   []ActionPermissionStatus `json:"scopes"`
}

type PermissionReview struct {
	ReviewID string `json:"review_id"`
	Revision string `json:"revision"`
	State    string `json:"state"`
}

type RecipeResult struct {
	ResultHandle string `json:"result_handle"`
	State        string `json:"state"`
}

func permissionRequest[T any](ctx context.Context, c *Client, protocol, method string, payload any) (T, error) {
	var result T
	raw, err := c.Request(ctx, protocol, method, payload)
	if err != nil {
		return result, privateServiceError(protocol, err)
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, &RPCError{"invalid_response", "Invalid permission response"}
	}
	return result, nil
}
func (c *Client) GetPermissions(ctx context.Context) (PermissionList, error) {
	return permissionRequest[PermissionList](ctx, c, PermissionsProtocol, "get-permissions", map[string]any{})
}
func (c *Client) HasPermission(ctx context.Context, action string, scope map[string]string) (PermissionStatus, error) {
	return permissionRequest[PermissionStatus](ctx, c, PermissionsProtocol, "has-permission", map[string]any{"action": action, "scope": scope})
}
func (c *Client) RequestPermissions(ctx context.Context, activation string, scopes []map[string]string) (PermissionReview, error) {
	return permissionRequest[PermissionReview](ctx, c, PermissionsProtocol, "request-permissions", map[string]any{"activation_token": activation, "scopes": scopes})
}

// SendExampleWebhook opens a review for a fixed local HTTPS ping. The PEM is the listener’s public session certificate.
func (c *Client) SendExampleWebhook(ctx context.Context, activation, certificate string) (RecipeResult, error) {
	return permissionRequest[RecipeResult](ctx, c, HTTPBrokerProtocol, "manager-webhook", map[string]string{"activation_token": activation, "certificate": certificate})
}

// SendLocalWebhook asks Manager to review a ping to the declared local HTTP origin.
func (c *Client) SendLocalWebhook(ctx context.Context, activation, origin string) (RecipeResult, error) {
	return permissionRequest[RecipeResult](ctx, c, HTTPBrokerProtocol, "manager-local-webhook", map[string]string{"activation_token": activation, "origin": origin})
}

// SummarizeClipboard opens the Manager-owned transfer review. Only Manager receives the text.
func (c *Client) SummarizeClipboard(ctx context.Context, activation string) (RecipeResult, error) {
	return permissionRequest[RecipeResult](ctx, c, HTTPBrokerProtocol, "clipboard-summary", map[string]string{"activation_token": activation})
}

type ClipboardText struct {
	Text     string `json:"text"`
	Revision string `json:"revision"`
}

func (ClipboardText) Format(w fmt.State, _ rune) { _, _ = io.WriteString(w, "[Clipboard text]") }

type CompletionStatus struct {
	State string `json:"state"`
}

func (c *Client) ReadClipboardText(ctx context.Context, activation string) (ClipboardText, error) {
	return permissionRequest[ClipboardText](ctx, c, PermissionsProtocol, "read-clipboard-text", map[string]string{"activation_token": activation})
}
func (c *Client) ShowTextResult(ctx context.Context, activation, text string) (RecipeResult, error) {
	return permissionRequest[RecipeResult](ctx, c, PermissionsProtocol, "show-text-result", map[string]string{"activation_token": activation, "text": text})
}
func (c *Client) NotifyCompletion(ctx context.Context, activation string) (CompletionStatus, error) {
	return permissionRequest[CompletionStatus](ctx, c, PermissionsProtocol, "notify-completion", map[string]string{"activation_token": activation})
}

const VaultReferencesProtocol = "plugin-vault-references-v1"

type VaultEntryName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type VaultEntryPage struct {
	Entries []VaultEntryName `json:"entries"`
	Next    *int             `json:"next"`
}

func (c *Client) ListVaultEntries(ctx context.Context, action string, scope map[string]string, start int) (VaultEntryPage, error) {
	return permissionRequest[VaultEntryPage](ctx, c, VaultReferencesProtocol, "list-vault-entries", map[string]any{"action": action, "scope": scope, "start": start})
}
func (c *Client) SelectVaultEntry(ctx context.Context, activation, action string, scope map[string]string) (PermissionReview, error) {
	return permissionRequest[PermissionReview](ctx, c, VaultReferencesProtocol, "select-vault-entry", map[string]any{"action": action, "scope": scope, "activation_token": activation})
}

// RunVaultRecipe asks Manager to review an exact reference and fixed destination.
func (c *Client) RunVaultRecipe(ctx context.Context, activation string, scope map[string]string, entryRef string) (RecipeResult, error) {
	method := scope["recipe"]
	if method != "clipboard-summary" {
		return RecipeResult{}, &RPCError{"invalid_request", "Unknown Vault recipe"}
	}
	return permissionRequest[RecipeResult](ctx, c, HTTPBrokerProtocol, method, map[string]any{"scope": scope, "entry_ref": entryRef, "activation_token": activation})
}

// PermissionNotice contains only the fields published by Manager for this terminal or change event.
type PermissionNotice struct {
	Event        string              `json:"event"`
	RequestID    string              `json:"request_id,omitempty"`
	ReviewID     string              `json:"review_id,omitempty"`
	Outcome      string              `json:"outcome,omitempty"`
	Revision     string              `json:"revision,omitempty"`
	Action       string              `json:"action,omitempty"`
	Scopes       []map[string]string `json:"scopes,omitempty"`
	Entry        *VaultEntryName     `json:"entry,omitempty"`
	ResultHandle string              `json:"result_handle,omitempty"`
}

func (e Event) PermissionNotice() (PermissionNotice, bool) {
	var n PermissionNotice
	switch e.Event {
	case "streamDockManagerPermissionRequestResult", "streamDockManagerPermissionsChanged", "streamDockManagerVaultSelectionResult":
		if json.Unmarshal(e.Raw, &n) != nil || n.Event != e.Event {
			return PermissionNotice{}, false
		}
		return n, true
	default:
		return PermissionNotice{}, false
	}
}

// NotificationsProtocol identifies Manager's plugin-message notification support
const NotificationsProtocol = "plugin-notifications-v1"

type NotificationLevel string

const (
	NotificationInfo    NotificationLevel = "info"
	NotificationSuccess NotificationLevel = "success"
	NotificationWarning NotificationLevel = "warning"
	NotificationError   NotificationLevel = "error"
)

// CreateNotification sends a plain-text notice under the reviewed action scope
func (c *Client) CreateNotification(ctx context.Context, action string, level NotificationLevel, message string) error {
	if !c.Supports(NotificationsProtocol) {
		return &UnsupportedFeatureError{NotificationsProtocol}
	}
	switch level {
	case NotificationInfo, NotificationSuccess, NotificationWarning, NotificationError:
	default:
		return &RPCError{"invalid_request", "Invalid notification level"}
	}
	if !validVaultID(action, 256) || strings.TrimSpace(message) == "" || !utf8.ValidString(message) {
		return &RPCError{"invalid_request", "Invalid notification action or message"}
	}
	if len(message) > 512 {
		return &RPCError{"too_large", "Notification message exceeds 512 UTF-8 bytes"}
	}
	receipt, err := permissionRequest[CompletionStatus](ctx, c, PermissionsProtocol, "create-notification", map[string]string{"action": action, "level": string(level), "message": message})
	if err != nil {
		return err
	}
	if receipt.State != "created" {
		return &RPCError{"invalid_response", "Invalid notification receipt"}
	}
	return nil
}
