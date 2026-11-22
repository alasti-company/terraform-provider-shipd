package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type ApprovalPolicyMatch struct {
	ProjectIDs            []string `json:"projectIds,omitempty"`
	EnvironmentIDs        []string `json:"environmentIds,omitempty"`
	EnvironmentProduction *bool    `json:"environmentProduction,omitempty"`
	MinRolloutIncrease    *float64 `json:"minRolloutIncrease,omitempty"`
}

type ApprovalPolicyConfig struct {
	Name              string              `json:"name"`
	Description       *string             `json:"description"`
	Enabled           bool                `json:"enabled"`
	Priority          int64               `json:"priority"`
	Match             ApprovalPolicyMatch `json:"match"`
	RequiredApprovals int64               `json:"requiredApprovals"`
	ReviewerRoles     []string            `json:"reviewerRoles"`
	ReviewerTeamIDs   []string            `json:"reviewerTeamIds"`
	AllowSelfApproval bool                `json:"allowSelfApproval"`
}

type ApprovalPolicy struct {
	ApprovalPolicyConfig
	ID        string `json:"id"`
	UpdatedAt string `json:"updatedAt"`
}

func (p *ApprovalPolicy) UnmarshalJSON(data []byte) error {
	type policy ApprovalPolicy
	var decoded policy
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, decoded.UpdatedAt); err != nil ||
		decoded.ID == "" || decoded.Name == "" || decoded.RequiredApprovals < 1 ||
		decoded.ReviewerRoles == nil || decoded.ReviewerTeamIDs == nil {
		return errors.New("invalid or incomplete shipd approval policy")
	}
	*p = ApprovalPolicy(decoded)
	return nil
}

func (c *Client) ApprovalPolicy(ctx context.Context, id string) (ApprovalPolicy, error) {
	var policies []ApprovalPolicy
	if err := c.RPC(ctx, "approval/listPolicies", struct{}{}, &policies, false); err != nil {
		return ApprovalPolicy{}, err
	}
	for _, policy := range policies {
		if policy.ID == id {
			return policy, nil
		}
	}
	return ApprovalPolicy{}, ErrNotFound
}

func (c *Client) CreateApprovalPolicy(ctx context.Context, input ApprovalPolicyConfig) (ApprovalPolicy, error) {
	var result ApprovalPolicy
	err := c.rpc(ctx, "approval/createPolicyConfig", input, &result, true, rpcOptions{attempts: 3, retryDecodeErrors: true})
	return result, err
}

type approvalPolicyPrecondition struct {
	PolicyID          string `json:"policyId"`
	ExpectedUpdatedAt string `json:"expectedUpdatedAt"`
}

func (c *Client) UpdateApprovalPolicy(ctx context.Context, id, updatedAt string, config ApprovalPolicyConfig) (ApprovalPolicy, error) {
	input := struct {
		approvalPolicyPrecondition
		ApprovalPolicyConfig
	}{approvalPolicyPrecondition{id, updatedAt}, config}
	var result ApprovalPolicy
	err := c.rpc(ctx, "approval/updatePolicyConfig", input, &result, true, rpcOptions{attempts: 3, retryDecodeErrors: true})
	if err == nil && result.ID != id {
		err = errors.New("shipd returned a different approval policy than requested")
	}
	return result, err
}

func (c *Client) DeleteApprovalPolicy(ctx context.Context, id, updatedAt string) error {
	var result struct {
		OK bool `json:"ok"`
	}
	err := c.RPC(ctx, "approval/deletePolicyConfig", approvalPolicyPrecondition{id, updatedAt}, &result, true)
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound &&
		apiErr.Code == "not_found" && apiErr.Message == "Approval policy not found":
		return nil
	case err != nil:
		return err
	case !result.OK:
		return errors.New("shipd did not confirm approval policy deletion")
	default:
		return nil
	}
}
