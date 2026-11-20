package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

type ProjectGrantTarget struct {
	ProjectID      string `json:"projectId"`
	OrganizationID string `json:"organizationId,omitempty"`
	Kind           string `json:"kind"`
	PrincipalID    string `json:"principalId"`
}

// A nil grant means there is no assignment. A nil role on a team assignment
// means the role is inherited.
type ProjectGrant struct {
	Role *string `json:"role"`
}

func (c *Client) GetProjectGrant(ctx context.Context, target ProjectGrantTarget) (string, *ProjectGrant, error) {
	return c.projectGrant(ctx, "project/getAccessGrant", target, target, false)
}

func (c *Client) ChangeProjectGrant(ctx context.Context, target ProjectGrantTarget, expected, grant *ProjectGrant) (string, *ProjectGrant, error) {
	input := struct {
		ProjectGrantTarget
		Expected *ProjectGrant `json:"expected"`
		Grant    *ProjectGrant `json:"grant"`
	}{target, expected, grant}
	return c.projectGrant(ctx, "project/changeAccessGrant", target, input, true)
}

func (c *Client) projectGrant(ctx context.Context, operation string, target ProjectGrantTarget, input any, mutation bool) (string, *ProjectGrant, error) {
	var result struct {
		OrganizationID string          `json:"organizationId"`
		Grant          json.RawMessage `json:"grant"`
	}
	if err := c.RPC(ctx, operation, input, &result, mutation); err != nil {
		return "", nil, err
	}
	if result.OrganizationID == "" || (target.OrganizationID != "" && target.OrganizationID != result.OrganizationID) {
		return "", nil, errors.New("invalid organization in shipd project grant response")
	}
	if bytes.Equal(result.Grant, []byte("null")) {
		return result.OrganizationID, nil, nil
	}
	var grant struct {
		Role json.RawMessage `json:"role"`
	}
	if err := json.Unmarshal(result.Grant, &grant); err != nil || len(grant.Role) == 0 {
		return "", nil, errors.New("invalid grant in shipd project grant response")
	}
	var role *string
	if err := json.Unmarshal(grant.Role, &role); err != nil ||
		(role == nil && target.Kind != "team") ||
		(role != nil && *role != "viewer" && *role != "editor") {
		return "", nil, errors.New("invalid role in shipd project grant response")
	}
	return result.OrganizationID, &ProjectGrant{Role: role}, nil
}

// A missing API route or principal is different from a missing project.
func IsProjectGrantProjectNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound &&
		apiErr.Code == "not_found" && apiErr.Message == "Project not found"
}
