package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

type AuditSettings struct {
	RetentionDays        *int64
	DefaultRetentionDays int64
	MinimumRetentionDays int64
	LegalHold            bool
}

func (s *AuditSettings) UnmarshalJSON(data []byte) error {
	var result struct {
		RetentionDays        json.RawMessage `json:"retentionDays"`
		DefaultRetentionDays *int64          `json:"defaultRetentionDays"`
		MinimumRetentionDays *int64          `json:"minimumRetentionDays"`
		LegalHold            *bool           `json:"legalHold"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	if len(result.RetentionDays) == 0 || result.DefaultRetentionDays == nil ||
		result.MinimumRetentionDays == nil || result.LegalHold == nil {
		return errors.New("incomplete shipd audit settings")
	}
	var days *int64
	if err := json.Unmarshal(result.RetentionDays, &days); err != nil {
		return err
	}
	*s = AuditSettings{days, *result.DefaultRetentionDays, *result.MinimumRetentionDays, *result.LegalHold}
	return nil
}

func (c *Client) AuditSettings(ctx context.Context) (AuditSettings, error) {
	var result AuditSettings
	err := c.RPC(ctx, "audit/getSettings", struct{}{}, &result, false)
	return result, err
}

func (c *Client) UpdateAuditSettings(ctx context.Context, days *int64) (AuditSettings, error) {
	input := struct {
		RetentionDays *int64 `json:"retentionDays"`
	}{days}
	var result AuditSettings
	err := c.RPC(ctx, "audit/updateSettings", input, &result, true)
	return result, err
}

// AuditDestinationConfig contains the supported nonsecret API fields. Pointer
// fields allow a patch to send an empty prefix or event list explicitly.
type AuditDestinationConfig struct {
	URL         string    `json:"url,omitempty"`
	Events      *[]string `json:"events,omitempty"`
	Format      string    `json:"format,omitempty"`
	Endpoint    string    `json:"endpoint,omitempty"`
	Region      string    `json:"region,omitempty"`
	Bucket      string    `json:"bucket,omitempty"`
	Prefix      *string   `json:"prefix,omitempty"`
	AccessKeyID string    `json:"accessKeyId,omitempty"`
}

type AuditDestination struct {
	ID      string                 `json:"id"`
	Type    string                 `json:"type"`
	Name    string                 `json:"name"`
	Enabled *bool                  `json:"enabled"`
	Config  AuditDestinationConfig `json:"config"`
}

type auditDestinationWriteConfig struct {
	AuditDestinationConfig
	Secret          string `json:"secret,omitempty"`
	Token           string `json:"token,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
}

func destinationWriteConfig(kind string, config AuditDestinationConfig, credential string) auditDestinationWriteConfig {
	input := auditDestinationWriteConfig{AuditDestinationConfig: config}
	switch kind {
	case "webhook":
		input.Secret = credential
	case "siem":
		input.Token = credential
	case "s3":
		input.SecretAccessKey = credential
	}
	return input
}

func (c *Client) AuditDestination(ctx context.Context, id string) (AuditDestination, error) {
	var destinations []AuditDestination
	if err := c.RPC(ctx, "audit/listDestinations", struct{}{}, &destinations, false); err != nil {
		return AuditDestination{}, err
	}
	for _, d := range destinations {
		if d.ID == "" {
			return AuditDestination{}, errors.New("invalid shipd audit destination list")
		}
		if d.ID == id {
			return d, nil
		}
	}
	return AuditDestination{}, ErrNotFound
}

func (c *Client) CreateAuditDestination(ctx context.Context, kind, name string, enabled bool, config AuditDestinationConfig, credential string) (AuditDestination, error) {
	input := struct {
		Type    string                      `json:"type"`
		Name    string                      `json:"name"`
		Enabled bool                        `json:"enabled"`
		Config  auditDestinationWriteConfig `json:"config"`
	}{kind, name, enabled, destinationWriteConfig(kind, config, credential)}
	var result AuditDestination
	// Webhook creation echoes the signing secret. Never ask the API to persist
	// the response for replay, and never retry a create with an ambiguous outcome.
	err := c.rpc(ctx, "audit/createDestination", input, &result, false, rpcOptions{attempts: 1})
	return result, err
}

func (c *Client) UpdateAuditDestination(ctx context.Context, id, kind string, name *string, enabled *bool, config *AuditDestinationConfig, credential string) (AuditDestination, error) {
	input := struct {
		ID      string                       `json:"id"`
		Name    *string                      `json:"name,omitempty"`
		Enabled *bool                        `json:"enabled,omitempty"`
		Config  *auditDestinationWriteConfig `json:"config,omitempty"`
	}{ID: id, Name: name, Enabled: enabled}
	if config != nil {
		writeConfig := destinationWriteConfig(kind, *config, credential)
		input.Config = &writeConfig
	}
	var result AuditDestination
	err := c.RPC(ctx, "audit/updateDestination", input, &result, true)
	if err == nil && result.ID != id {
		err = errors.New("shipd returned a different audit destination than requested")
	}
	return result, err
}

func (c *Client) DeleteAuditDestination(ctx context.Context, id string) error {
	input := struct {
		ID string `json:"id"`
	}{id}
	var result struct {
		Success bool `json:"success"`
	}
	err := c.RPC(ctx, "audit/deleteDestination", input, &result, true)
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound &&
		apiErr.Code == "not_found" && apiErr.Message == "Destination not found":
		return nil
	case err != nil:
		return err
	case !result.Success:
		return errors.New("shipd did not confirm audit destination deletion")
	default:
		return nil
	}
}
