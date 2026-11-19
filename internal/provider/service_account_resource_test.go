package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestTerraformServiceAccountLifecycle(t *testing.T) {
	const address = "shipd_service_account.test"
	const accountID = "account-test"
	const organizationID = "org-test"
	var mu sync.Mutex
	var account map[string]any
	var mutations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		operation := strings.TrimPrefix(r.URL.Path, "/api/v1/rpc/")
		write := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": data}) }
		fail := func(status int, code, message string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": code, "message": message},
			})
		}
		token := r.Header.Get("x-api-key")
		if token != "test-token" && token != "other-token" {
			fail(401, "unauthenticated", "Authentication required")
			return
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if operation == "me" {
			write(map[string]any{"organization": map[string]string{"id": organizationID}})
			return
		}
		if operation != "serviceAccount/get" {
			if r.Header.Get("Idempotency-Key") == "" {
				t.Errorf("%s missing idempotency key", operation)
			}
			mutations = append(mutations, operation)
		}
		if token == "other-token" {
			if input["orgId"] != organizationID {
				t.Errorf("refresh with new credentials omitted org guard: %v", input)
			}
			fail(403, "forbidden", "Cannot manage service accounts for another organization")
			return
		}
		if operation != "serviceAccount/create" && (account == nil || input["serviceAccountId"] != accountID) {
			fail(404, "not_found", "Service account not found")
			return
		}
		switch operation {
		case "serviceAccount/create":
			if input["orgId"] != organizationID || input["enabled"] != false || account != nil {
				t.Errorf("unexpected account create input: %v", input)
			}
			account = map[string]any{
				"id": accountID, "organizationId": organizationID, "disabledAt": "2026-01-01T00:00:00Z",
				"name": input["name"], "description": input["description"],
			}
		case "serviceAccount/get":
		case "serviceAccount/update":
			if input["enabled"] != true || input["name"] != "Updated" {
				t.Errorf("metadata and enabled state should update together: %v", input)
			}
			if value, present := input["description"]; !present || value != nil {
				t.Errorf("clearing description should send explicit null: %v", input)
			}
			account["name"], account["description"], account["disabledAt"] = input["name"], nil, nil
		case "serviceAccount/disable":
			account["disabledAt"] = "2026-01-02T00:00:00Z"
		case "serviceAccount/enable":
			account["disabledAt"] = nil
		case "serviceAccount/delete":
			account = nil
			write(map[string]bool{"success": true})
			return
		default:
			t.Errorf("unexpected operation: %s", operation)
			fail(404, "not_found", "Route not found")
			return
		}
		write(account)
	}))
	defer server.Close()

	config := func(token, name, fields string) string {
		return fmt.Sprintf(`
provider "shipd" {
  api_url = %q
  api_key = %q
}

resource "shipd_service_account" "test" {
  name = %q
  %s
}
`, server.URL, token, name, fields)
	}
	initial := config("test-token", "Initial", "enabled = false\ndescription = \"Builds\"")
	updated := config("test-token", "Updated", "")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: factories(), PreventPostDestroyRefresh: true,
		Steps: []resource.TestStep{
			{Config: initial, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(address, "id", accountID),
				resource.TestCheckResourceAttr(address, "organization_id", organizationID),
				resource.TestCheckResourceAttr(address, "description", "Builds"),
				resource.TestCheckResourceAttr(address, "enabled", "false"),
			)},
			{ResourceName: address, ImportState: true, ImportStateVerify: true, ImportStateId: " " + accountID + " "},
			{Config: initial, PlanOnly: true},
			{Config: config("other-token", "Initial", "enabled = false\ndescription = \"Builds\""),
				PlanOnly: true, ExpectError: regexp.MustCompile("shipd API 403")},
			{Config: updated, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(address, "id", accountID),
				resource.TestCheckNoResourceAttr(address, "description"),
				resource.TestCheckResourceAttr(address, "enabled", "true"),
			)},
			{Config: config("test-token", "Updated", "enabled = false"),
				Check: resource.TestCheckResourceAttr(address, "enabled", "false")},
			{Config: updated, Check: resource.TestCheckResourceAttr(address, "enabled", "true")},
		},
		CheckDestroy: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if account != nil {
				return fmt.Errorf("service account still exists")
			}
			return nil
		},
	})
	mu.Lock()
	defer mu.Unlock()
	want := "serviceAccount/create,serviceAccount/update,serviceAccount/disable,serviceAccount/enable,serviceAccount/delete"
	if got := strings.Join(mutations, ","); got != want {
		t.Errorf("mutations: got %s, want %s", got, want)
	}
}
