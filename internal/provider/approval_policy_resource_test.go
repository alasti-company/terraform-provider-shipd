package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestTerraformApprovalPolicyLifecycle(t *testing.T) {
	const address = "shipd_approval_policy.test"
	policy := &client.ApprovalPolicy{
		ID: "existing", UpdatedAt: "2026-11-21T00:00:00.000Z",
		ApprovalPolicyConfig: client.ApprovalPolicyConfig{
			Name: "Release review", Priority: 100, RequiredApprovals: 1,
			Match: client.ApprovalPolicyMatch{
				ProjectIDs: []string{"project-b", "project-a", "project-a"}, EnvironmentIDs: []string{"environment-b", "environment-a"},
				EnvironmentProduction: new(false), MinRolloutIncrease: new(0.0),
			},
			ReviewerRoles: []string{"owner", "admin"}, ReviewerTeamIDs: []string{"team-b", "team-a"},
		},
	}
	var mu sync.Mutex
	creates, updates, deletes := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var input struct {
			client.ApprovalPolicyConfig
			PolicyID          string `json:"policyId"`
			ExpectedUpdatedAt string `json:"expectedUpdatedAt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode approval policy input: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		write := func(value any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": value})
		}
		forbidden := func(message string) {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "forbidden", "message": message},
			})
		}
		switch r.URL.Path {
		case "/api/v1/rpc/approval/listPolicies":
			out := []*client.ApprovalPolicy{}
			if policy != nil {
				out = append(out, policy)
			}
			write(out)
		case "/api/v1/rpc/approval/createPolicyConfig":
			creates++
			if policy != nil || input.Match.EnvironmentProduction != nil || input.Match.MinRolloutIncrease != nil ||
				input.ReviewerTeamIDs == nil || len(input.ReviewerTeamIDs) != 0 {
				t.Errorf("unexpected create configuration: %+v", input)
			}
			policy = &client.ApprovalPolicy{
				ID: "created", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
				ApprovalPolicyConfig: input.ApprovalPolicyConfig,
			}
			write(policy)
		case "/api/v1/rpc/approval/updatePolicyConfig", "/api/v1/rpc/approval/deletePolicyConfig":
			if r.Header.Get("Idempotency-Key") == "" || policy == nil ||
				input.PolicyID != policy.ID || input.ExpectedUpdatedAt != policy.UpdatedAt {
				t.Errorf("missing mutation precondition: %+v", input)
				w.WriteHeader(http.StatusConflict)
				return
			}
			if r.URL.Path == "/api/v1/rpc/approval/deletePolicyConfig" {
				if policy.Enabled {
					forbidden("A service account cannot delete an enabled approval policy.")
					return
				}
				deletes++
				policy = nil
				write(map[string]bool{"ok": true})
				return
			}
			if policy.Enabled && (!input.Enabled || input.RequiredApprovals < policy.RequiredApprovals) {
				forbidden("A service account cannot weaken or disable an enabled approval policy.")
				return
			}
			updates++
			policy.ApprovalPolicyConfig = input.ApprovalPolicyConfig
			slices.Reverse(policy.Match.ProjectIDs)
			policy.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			write(policy)
		default:
			t.Errorf("unexpected RPC %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	provider := fmt.Sprintf("provider \"shipd\" {\napi_url = %q\napi_key = \"test-token\"\n}\n", server.URL)
	config := provider + `
resource "shipd_approval_policy" "test" {
  name                   = "Release review"
  enabled                = false
  project_ids            = ["project-a", "project-b"]
  environment_ids        = ["environment-a", "environment-b"]
  environment_production = false
  min_rollout_increase   = 0
  reviewer_roles         = ["admin", "owner"]
  reviewer_team_ids      = ["team-a", "team-b"]
}
`
	policyConfig := func(enabled bool, requiredApprovals int) string {
		return provider + fmt.Sprintf(`
resource "shipd_approval_policy" "test" {
  name               = "Staged review"
  enabled            = %t
  project_ids        = ["project-a", "project-b"]
  required_approvals = %d
  reviewer_roles     = ["owner"]
}
`, enabled, requiredApprovals)
	}
	changed := policyConfig(false, 2)
	strengthened := policyConfig(true, 3)
	enabledPolicy := resource.ComposeTestCheckFunc(
		resource.TestCheckResourceAttr(address, "id", "created"),
		resource.TestCheckResourceAttr(address, "enabled", "true"),
		resource.TestCheckResourceAttr(address, "required_approvals", "3"),
	)
	deniedUpdate := regexp.MustCompile(`(?s)Cannot update approval policy.*cannot weaken or disable`)
	deniedDestroy := regexp.MustCompile(`(?s)Cannot delete approval policy.*cannot delete an enabled\s+approval policy`)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: config + `
import {
  to = shipd_approval_policy.test
  id = "existing"
}
`, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(address, "id", "existing"),
				resource.TestCheckResourceAttr(address, "project_ids.#", "2"),
				resource.TestCheckResourceAttr(address, "environment_production", "false"),
				resource.TestCheckResourceAttr(address, "min_rollout_increase", "0"),
			)},
			{Config: config, PlanOnly: true},
			{Config: changed, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(address, "id", "existing"),
				resource.TestCheckResourceAttr(address, "reviewer_team_ids.#", "0"),
				resource.TestCheckNoResourceAttr(address, "environment_production"),
				resource.TestCheckNoResourceAttr(address, "min_rollout_increase"),
			)},
			{ResourceName: address, ImportState: true, ImportStateVerify: true},
			{Config: provider},
			{Config: changed, Check: resource.TestCheckResourceAttr(address, "id", "created")},
			{Config: policyConfig(true, 2), Check: resource.TestCheckResourceAttr(address, "enabled", "true")},
			{Config: strengthened, Check: enabledPolicy},
			{Config: policyConfig(true, 1), ExpectError: deniedUpdate},
			{Config: policyConfig(false, 3), ExpectError: deniedUpdate},
			{Config: provider, ExpectError: deniedDestroy},
			{Config: strengthened, PlanOnly: true, Check: enabledPolicy},
			{Config: provider, PreConfig: func() {
				mu.Lock()
				defer mu.Unlock()
				// A human disables the policy outside Terraform before destroy.
				policy.Enabled = false
				policy.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}},
		},
		CheckDestroy: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if policy != nil || creates != 1 || updates != 3 || deletes != 2 {
				return fmt.Errorf("unexpected approval policy lifecycle counts: %d creates, %d updates, %d deletes", creates, updates, deletes)
			}
			return nil
		},
	})
}

func TestTerraformApprovalPolicyValidation(t *testing.T) {
	for _, item := range []struct {
		input string
		error string
	}{
		{`required_approvals = 0`, "must be between 1 and 20"},
		{`reviewer_roles = ["editor"]`, "value must be one of"},
		{`min_rollout_increase = 101`, "must be between 0\\.000000 and 100\\.000000"},
		{`sources = ["api"]`, "Unsupported argument"},
	} {
		t.Run(item.input, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{{
					Config: `
provider "shipd" { api_key = "test-token" }
resource "shipd_approval_policy" "test" {
  name = "Release review"
  ` + item.input + `
}
`,
					PlanOnly: true, ExpectError: regexp.MustCompile("(?i)" + item.error),
				}},
			})
		})
	}
}
