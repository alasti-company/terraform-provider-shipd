package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestProjectGrantLifecycle(t *testing.T) {
	for _, kind := range []string{"member", "team"} {
		t.Run(kind, func(t *testing.T) {
			const address = "shipd_project_access_grant.test"
			var mu sync.Mutex
			edits := 0
			unrelated := &client.ProjectGrant{Role: new("editor")}
			grant := &client.ProjectGrant{Role: new("viewer")}
			if kind == "team" {
				grant.Role = nil
			}
			grants := map[string]*client.ProjectGrant{"unrelated": unrelated, "principal": grant}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					client.ProjectGrantTarget
					Expected *client.ProjectGrant `json:"expected"`
					Grant    *client.ProjectGrant `json:"grant"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil ||
					input.ProjectID != "project" || input.Kind != kind ||
					input.PrincipalID != "principal" ||
					(input.OrganizationID != "" && input.OrganizationID != "org") ||
					r.Header.Get("x-api-key") != "test-token" {
					t.Errorf("unexpected request %s: %+v", r.URL.Path, input)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/api/v1/rpc/project/getAccessGrant":
				case "/api/v1/rpc/project/changeAccessGrant":
					if r.Header.Get("Idempotency-Key") == "" {
						t.Error("missing Idempotency-Key")
					}
					current := grants[input.PrincipalID]
					if !equalTestGrant(current, input.Expected) && !(current == nil && input.Grant == nil) {
						w.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Import the existing grant"}})
						return
					}
					edits++
					if input.Grant == nil {
						delete(grants, input.PrincipalID)
					} else {
						grants[input.PrincipalID] = input.Grant
					}
				default:
					w.WriteHeader(404)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
					"organizationId": "org", "grant": grants[input.PrincipalID],
				}})
			}))
			t.Cleanup(server.Close)
			provider := fmt.Sprintf(`
provider "shipd" {
  api_url = %q
  api_key = "test-token"
}
`, server.URL)
			config := func(role string) string {
				return provider + fmt.Sprintf(`
resource "shipd_project_access_grant" "test" {
  project_id     = "project"
  principal_type = %q
  principal_id   = "principal"
  role           = %q
}
`, kind, role)
			}
			id := "project/" + kind + "/principal"
			original := grantRole(grant)
			importConfig := config(original) + fmt.Sprintf(`
import {
  to = %s
  id = %q
}
`, address, id)
			resource.Test(t, resource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{
					{Config: config(original), ExpectError: regexp.MustCompile("Import the existing grant")},
					{Config: importConfig, Check: resource.ComposeTestCheckFunc(
						resource.TestCheckResourceAttr(address, "id", id),
						resource.TestCheckResourceAttr(address, "organization_id", "org"),
						resource.TestCheckResourceAttr(address, "role", original),
					)},
					{Config: config(original), PlanOnly: true},
					{Config: config("editor"), Check: resource.TestCheckResourceAttr(address, "role", "editor")},
					{Config: config("editor"), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() {
						mu.Lock()
						defer mu.Unlock()
						grants["principal"] = &client.ProjectGrant{Role: new("viewer")}
					}},
					{Config: config("editor"), Check: resource.TestCheckResourceAttr(address, "role", "editor")},
					{Config: config("editor"), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() {
						mu.Lock()
						defer mu.Unlock()
						delete(grants, "principal")
					}},
					{Config: config("editor")},
				},
				CheckDestroy: func(_ *terraform.State) error {
					mu.Lock()
					defer mu.Unlock()
					if grants["principal"] != nil || grants["unrelated"] != unrelated || edits != 4 {
						return fmt.Errorf("expected four changes and only the unrelated grant to remain, got %d changes and %v", edits, grants)
					}
					return nil
				},
			})
		})
	}
}

func equalTestGrant(a, b *client.ProjectGrant) bool {
	if a == nil || b == nil {
		return a == b
	}
	return grantRole(a) == grantRole(b)
}
