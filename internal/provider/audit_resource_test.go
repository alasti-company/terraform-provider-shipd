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

	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestTerraformAuditLifecycle(t *testing.T) {
	const settings = "shipd_audit_settings.organization"
	var mu sync.Mutex
	retention := any(float64(30))
	var retentionUpdates, destinationCreates, destinationUpdates, destinationDeletes int
	destinations := map[string]map[string]any{
		"unmanaged": {
			"id": "unmanaged", "type": "webhook", "name": "Unmanaged", "enabled": true,
			"config": map[string]any{"url": "https://receiver.example/unmanaged", "secret": "unmanaged-secret"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("x-api-key") != "test-token" {
			t.Error("audit operation is missing machine credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		write := func(data any) {
			if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
				t.Error(err)
			}
		}
		writeSettings := func() {
			write(map[string]any{
				"retentionDays": retention, "defaultRetentionDays": 365,
				"minimumRetentionDays": 14, "legalHold": false,
			})
		}
		switch r.URL.Path {
		case "/api/v1/rpc/me":
			write(map[string]any{"organization": map[string]string{"id": "test-org"}})
		case "/api/v1/rpc/audit/getSettings":
			writeSettings()
		case "/api/v1/rpc/audit/updateSettings":
			retentionUpdates++
			days, present := input["retentionDays"]
			if len(input) != 1 || !present {
				t.Errorf("unexpected retention patch: %v", input)
			}
			retention = days
			writeSettings()
		case "/api/v1/rpc/audit/listDestinations":
			list := make([]map[string]any, 0, len(destinations))
			for _, destination := range destinations {
				// Echo credential fields to verify that the provider ignores them.
				list = append(list, destination)
			}
			write(list)
		case "/api/v1/rpc/audit/createDestination":
			destinationCreates++
			if r.Header.Get("Idempotency-Key") != "" {
				t.Error("destination create requested durable replay")
			}
			kind := input["type"].(string)
			field := map[string]string{"webhook": "secret", "siem": "token", "s3": "secretAccessKey"}[kind]
			if input["config"].(map[string]any)[field] != "initial-fixture-secret" {
				t.Errorf("missing credential on %s creation", kind)
			}
			input["id"] = kind
			destinations[kind] = input
			write(input)
		case "/api/v1/rpc/audit/updateDestination":
			destinationUpdates++
			id := input["id"].(string)
			destination := destinations[id]
			if name, ok := input["name"]; ok {
				destination["name"] = name
			}
			if enabled, ok := input["enabled"]; ok {
				destination["enabled"] = enabled
			}
			if config, ok := input["config"]; ok {
				fields := config.(map[string]any)
				if id == "webhook" && fields["secret"] != "rotated-fixture-secret" {
					t.Error("webhook rotation did not supply the new secret")
				}
				if id == "s3" && (fields["secretAccessKey"] != nil || fields["prefix"] != "") {
					t.Error("S3 prefix removal must clear the prefix and preserve the credential")
				}
				old := destination["config"].(map[string]any)
				for key, value := range fields {
					old[key] = value
				}
			} else if id != "siem" {
				t.Errorf("missing config patch for %s", id)
			}
			if id == "siem" && (len(input) != 2 || input["name"] != "Updated SIEM") {
				t.Error("SIEM name update sent unrelated fields")
			}
			write(destination)
		case "/api/v1/rpc/audit/deleteDestination":
			destinationDeletes++
			delete(destinations, input["id"].(string))
			write(map[string]bool{"success": true})
		default:
			t.Errorf("unexpected RPC %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	initial := fmt.Sprintf(`
provider "shipd" {
  api_url = %q
  api_key = "test-token"
}
variable "credential" {
  type      = string
  sensitive = true
  ephemeral = true
  default   = "initial-fixture-secret"
}
resource "shipd_audit_settings" "organization" {
  organization_id = "test-org"
  retention_days = 30
}
resource "shipd_audit_destination" "webhook" {
  type = "webhook"
  name = "Webhook"
  url = "https://receiver.example/audit"
  events = ["flag.created"]
  credential_version = "v1"
  credential_wo = var.credential
}
resource "shipd_audit_destination" "siem" {
  type = "siem"
  name = "Original SIEM"
  url = "https://receiver.example/siem"
  format = "datadog"
  credential_version = "v1"
  credential_wo = var.credential
}
resource "shipd_audit_destination" "s3" {
  type = "s3"
  name = "S3"
  endpoint = "https://receiver.example/s3"
  region = "auto"
  bucket = "test"
  prefix = "audit"
  access_key_id = "fixture-access-key-id"
  credential_version = "v1"
  credential_wo = var.credential
}
`, server.URL)
	updated := strings.NewReplacer(
		"retention_days = 30", "retention_days = 0",
		"initial-fixture-secret", "rotated-fixture-secret",
		`events = ["flag.created"]`+"\n  "+`credential_version = "v1"`, `credential_version = "v2"`,
		"Original SIEM", "Updated SIEM",
		`prefix = "audit"`, "",
	).Replace(initial)
	destinationChecks := func(version string) resource.TestCheckFunc {
		checks := []resource.TestCheckFunc{
			resource.TestCheckResourceAttr(settings, "minimum_retention_days", "14"),
			resource.TestCheckResourceAttr(settings, "legal_hold", "false"),
			resource.TestCheckResourceAttr("shipd_audit_destination.webhook", "credential_version", version),
		}
		for _, kind := range []string{"webhook", "siem", "s3"} {
			address := "shipd_audit_destination." + kind
			checks = append(checks, resource.TestCheckResourceAttr(address, "id", kind), resource.TestCheckNoResourceAttr(address, "credential_wo"))
		}
		return resource.ComposeTestCheckFunc(checks...)
	}
	checkCounts := func(wantedRetention, wantedDestinations int) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if retentionUpdates != wantedRetention || destinationUpdates != wantedDestinations || destinationCreates != 3 {
				return fmt.Errorf("unexpected writes: %d retention updates, %d creates and %d destination updates", retentionUpdates, destinationCreates, destinationUpdates)
			}
			return nil
		}
	}
	steps := []resource.TestStep{{Config: initial, Check: resource.ComposeTestCheckFunc(destinationChecks("v1"), checkCounts(0, 0))}}
	steps = append(steps, resource.TestStep{ResourceName: settings, ImportState: true, ImportStateVerify: true, ImportStateId: "test-org"})
	for _, kind := range []string{"webhook", "siem", "s3"} {
		steps = append(steps, resource.TestStep{
			ResourceName: "shipd_audit_destination." + kind, ImportState: true, ImportStateVerify: true, ImportStateId: kind,
			ImportStateVerifyIgnore: []string{"credential_version"},
		})
	}
	steps = append(steps,
		resource.TestStep{Config: updated, Check: resource.ComposeTestCheckFunc(destinationChecks("v2"), checkCounts(1, 3),
			resource.TestCheckResourceAttr(settings, "retention_days", "0"),
			resource.TestCheckNoResourceAttr("shipd_audit_destination.webhook", "events"),
			resource.TestCheckNoResourceAttr("shipd_audit_destination.s3", "prefix"),
		)},
		resource.TestStep{Config: updated, Check: checkCounts(1, 3)},
		resource.TestStep{
			Config:   strings.ReplaceAll(updated, `access_key_id = "fixture-access-key-id"`, `access_key_id = "new-identifier"`),
			PlanOnly: true, ExpectError: regexp.MustCompile("Missing S3 credential rotation"),
		},
		resource.TestStep{
			Config: strings.Replace(updated, "  credential_version = \"v2\"\n  credential_wo = var.credential\n", "", 1),
			Check:  resource.ComposeTestCheckFunc(checkCounts(1, 3), resource.TestCheckNoResourceAttr("shipd_audit_destination.webhook", "credential_version")),
		},
	)
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: factories(), Steps: steps,
		CheckDestroy: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if retention != nil || retentionUpdates != 1 || destinationDeletes != 3 ||
				len(destinations) != 1 || destinations["unmanaged"]["name"] != "Unmanaged" {
				return fmt.Errorf("destroy changed retention or an unrelated destination")
			}
			return nil
		},
	})
}

func TestAuditDiagnostics(t *testing.T) {
	const scope = "organization:update"
	for _, tc := range []struct {
		name     string
		apiError client.APIError
		settings bool
		want     string
	}{
		{"permission", client.APIError{Status: http.StatusForbidden, Message: "echoed-fixture-secret"}, false, scope},
		{"plan", client.APIError{Status: http.StatusForbidden, Message: "Audit log is available on the Teams plan."}, false, "Teams and Enterprise"},
		{"destination quota", client.APIError{Status: http.StatusForbidden, Code: "quota_exceeded"}, false, "destination quota"},
		{"retention quota", client.APIError{Status: http.StatusForbidden, Code: "quota_exceeded"}, true, "retention exceeds"},
		{"retention hold", client.APIError{Status: http.StatusForbidden, Message: "Retention is protected by an organization hold or minimum retention policy."}, true, "legal_hold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := auditDiagnostic(&tc.apiError, scope, tc.settings)
			if !strings.Contains(got, tc.want) || strings.Contains(got, "echoed-fixture-secret") {
				t.Errorf("incorrect diagnostic: %s", got)
			}
		})
	}
}
