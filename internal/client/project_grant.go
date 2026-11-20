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
	result := projectGrantResponse{target: target}
	err := c.RPC(ctx, "project/getAccessGrant", target, &result, false)
	return result.organizationID, result.grant, err
}

func (c *Client) ChangeProjectGrant(ctx context.Context, target ProjectGrantTarget, expected, grant *ProjectGrant) (string, *ProjectGrant, error) {
	input := struct {
		ProjectGrantTarget
		Expected *ProjectGrant `json:"expected"`
		Grant    *ProjectGrant `json:"grant"`
	}{target, expected, grant}
	result := projectGrantResponse{target: target, desired: grant}
	err := c.rpc(ctx, "project/changeAccessGrant", input, &result, true, rpcOptions{attempts: 3, retryDecodeErrors: true})
	return result.organizationID, result.grant, err
}

type projectGrantResponse struct {
	target         ProjectGrantTarget
	desired        *ProjectGrant
	organizationID string
	grant          *ProjectGrant
}

func (r *projectGrantResponse) UnmarshalJSON(data []byte) error {
	var result struct {
		OrganizationID string          `json:"organizationId"`
		Grant          json.RawMessage `json:"grant"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	if result.OrganizationID == "" ||
		(r.target.OrganizationID != "" && r.target.OrganizationID != result.OrganizationID) {
		return errors.New("invalid organization in shipd project grant response")
	}
	var grant *ProjectGrant
	if !bytes.Equal(result.Grant, []byte("null")) {
		var fields struct {
			Role json.RawMessage `json:"role"`
		}
		if err := json.Unmarshal(result.Grant, &fields); err != nil || len(fields.Role) == 0 {
			return errors.New("invalid grant in shipd project grant response")
		}
		grant = &ProjectGrant{}
		if err := json.Unmarshal(fields.Role, &grant.Role); err != nil ||
			(grant.Role == nil && r.target.Kind != "team") ||
			(grant.Role != nil && *grant.Role != "viewer" && *grant.Role != "editor") {
			return errors.New("invalid role in shipd project grant response")
		}
	}
	if r.desired != nil && !sameProjectGrant(r.desired, grant) {
		return errors.New("shipd returned a different project grant than requested")
	}
	r.organizationID, r.grant = result.OrganizationID, grant
	return nil
}

func sameProjectGrant(a, b *ProjectGrant) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Role == nil || b.Role == nil {
		return a.Role == b.Role
	}
	return *a.Role == *b.Role
}

// A missing API route or principal is different from a missing project.
func IsProjectGrantProjectNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound &&
		apiErr.Code == "not_found" && apiErr.Message == "Project not found"
}
