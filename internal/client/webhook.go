package client

import (
	"context"
	"errors"
	"fmt"
)

type Webhook struct {
	ID            string   `json:"id"`
	EnvironmentID string   `json:"environmentId"`
	URL           string   `json:"url"`
	Enabled       bool     `json:"enabled"`
	Name          string   `json:"name,omitempty"`
	EventTypes    []string `json:"eventTypes,omitempty"`
}

func (c *Client) CreateWebhook(ctx context.Context, kind string, config Webhook) (Webhook, error) {
	input := map[string]any{
		"environmentId": config.EnvironmentID,
		"url":           config.URL,
		"enabled":       config.Enabled,
	}
	if kind == "eventWebhook" {
		input["name"] = config.Name
		input["eventTypes"] = config.EventTypes
	}
	var out Webhook
	err := c.RPC(ctx, kind+"/createConfig", input, &out, true)
	return out, webhookError(err)
}

func (c *Client) ReadWebhook(ctx context.Context, kind, environmentID, id string) (Webhook, error) {
	var items []Webhook
	err := c.RPC(ctx, kind+"/list", map[string]string{"environmentId": environmentID}, &items, false)
	var api *APIError
	if errors.As(err, &api) && api.Status == 404 && api.Code == "not_found" && api.Message == "Environment not found" {
		return Webhook{}, ErrNotFound
	}
	if err != nil {
		return Webhook{}, webhookError(err)
	}
	for _, item := range items {
		if item.ID == id && item.EnvironmentID == environmentID {
			return item, nil
		}
	}
	return Webhook{}, ErrNotFound
}

func (c *Client) UpdateWebhook(ctx context.Context, kind string, config Webhook) (Webhook, error) {
	input := webhookTarget(kind, config.EnvironmentID, config.ID)
	input["enabled"] = config.Enabled
	var out Webhook
	if kind == "eventWebhook" {
		input["eventTypes"] = config.EventTypes
		// This RPC also supports secret rotation, so the API excludes it from durable replay.
		err := c.rpc(ctx, kind+"/update", input, &out, false, 1)
		return out, webhookError(err)
	}
	input["url"] = config.URL
	err := c.RPC(ctx, kind+"/update", input, &out, true)
	return out, webhookError(err)
}

func (c *Client) DeleteWebhook(ctx context.Context, kind, environmentID, id string) error {
	input := webhookTarget(kind, environmentID, id)
	var err error
	if kind == "eventWebhook" {
		err = c.rpc(ctx, kind+"/delete", input, nil, false, 1)
	} else {
		err = c.RPC(ctx, kind+"/delete", input, nil, true)
	}
	var api *APIError
	if errors.As(err, &api) && api.Status == 404 && api.Code == "not_found" {
		return nil
	}
	return webhookError(err)
}

func webhookTarget(kind, environmentID, id string) map[string]any {
	idField := "destinationId"
	if kind == "eventWebhook" {
		idField = "endpointId"
	}
	return map[string]any{"environmentId": environmentID, idField: id}
}

// A destination URL can contain credentials, and an error body can echo it or a
// signing secret. Only include status and provider-authored guidance in diagnostics.
func webhookError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	var api *APIError
	if !errors.As(err, &api) {
		return errors.New("shipd webhook request failed or returned an invalid response. Check the configuration in shipd before retrying.")
	}
	detail := "Check the configuration in shipd before retrying."
	switch api.Status {
	case 401:
		detail = "Check the provider credential."
	case 403:
		detail = "Check the credential's webhook scopes and environment binding, and confirm your organization can use this webhook."
		if api.Code == "quota_exceeded" {
			detail = "The organization has reached its webhook endpoint allowance."
		}
	case 404:
		detail = "Check the environment and webhook IDs and confirm the API supports this operation."
	case 409:
		detail = "Refresh the plan and check for an existing webhook. An environment can have one snapshot destination."
	case 422:
		detail = "Check the destination URL and, for event webhooks, the name and event subscriptions."
	}
	return fmt.Errorf("shipd webhook request returned HTTP %d. %s", api.Status, detail)
}
