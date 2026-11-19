package provider

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestTerraformCredentialRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, register, get, revoke, target, fields string
		prefixLength, droppedResponses                      int
	}{
		{"sdk_key", "shipd_sdk_server_", "sdkKey/register", "sdkKey/get", "sdkKey/revoke", "keyId",
			"project_id = \"proj-test\"\nenvironment_id = \"env-test\"\nkey_type = \"server\"\nscopes = [\"snapshot:read\"]", 28, 1},
		{"service_account_token", "shipd_sa_", "serviceAccount/registerToken", "serviceAccount/getToken", "serviceAccount/revokeToken", "tokenId",
			"service_account_id = \"account-test\"\nscopes = { project = [\"read\"] }", 24, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "51f512f1-744b-4bde-8d35-8bd7c3753952"
			const issuer = "shipd_sa_TEST_ONLY_issuer"
			orgDigest := sha256.Sum256([]byte("org-test"))
			tag := hex.EncodeToString(orgDigest[:16])
			secret := tc.prefix + base64.RawURLEncoding.EncodeToString([]byte("a fake secret used in tests only")) + "." + tag
			digest := sha256.Sum256([]byte(secret))
			logPath := filepath.Join(t.TempDir(), "terraform.log")
			t.Setenv("TF_LOG", "TRACE")
			t.Setenv("TF_LOG_PATH", logPath)
			t.Setenv("SHIPD_API_KEY", issuer)
			t.Setenv("TF_VAR_credential", secret)

			var mu sync.Mutex
			var registered map[string]any
			var revoked bool
			var createCalls, writes int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				write := func(data any) {
					_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
				}
				if r.Header.Get("x-api-key") != issuer {
					t.Error("provider did not use bootstrap token")
				}
				var input map[string]any
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error("invalid request body")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if _, present := input["secret"]; present {
					t.Error("provider transmitted the plaintext credential")
				}
				if input["orgId"] != "org-test" {
					t.Error("credential operation omitted the organization guard")
				}
				switch strings.TrimPrefix(r.URL.Path, "/api/v1/rpc/") {
				case tc.register:
					if input["id"] != id || input["keyHash"] != hex.EncodeToString(digest[:]) ||
						input["keyPrefix"] != secret[:tc.prefixLength]+"."+tag || r.Header.Get("Idempotency-Key") != "" {
						t.Error("incorrect registration identity or replay mechanism")
					}
					createCalls++
					if registered == nil {
						registered = input
						writes++
					} else if !reflect.DeepEqual(registered, input) {
						t.Error("retry changed registration inputs")
					}
					if createCalls <= tc.droppedResponses {
						// Commit succeeded. The client never obtained a complete response.
						_, _ = w.Write([]byte("{"))
						return
					}
					write(map[string]any{"id": id, "organizationId": "org-test"})
				case tc.get:
					if registered == nil || input[tc.target] != id {
						t.Error("read did not target the registered credential")
					}
					write(map[string]any{"id": id, "revokedAt": nil})
				case tc.revoke:
					if registered == nil || input[tc.target] != id {
						t.Error("revocation did not target the registered credential")
					}
					revoked = true
					write(map[string]bool{"success": true})
				default:
					t.Error("unexpected credential operation")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			config := func(name, secretInput string) string {
				return fmt.Sprintf(`
provider "shipd" {
  api_url = %q
}

variable "credential" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "shipd_%s" "test" {
  organization_id = "org-test"
  id              = %q
  name            = %q
  expires_at      = %q
  secret_wo       = %s
  %s
}
`, server.URL, tc.name, id, name, expiry, secretInput, tc.fields)
			}
			address := "shipd_" + tc.name + ".test"
			initial := config("Workload", "var.credential")
			steps := []resource.TestStep{
				{Config: initial, Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(address, "id", id),
					resource.TestCheckResourceAttr(address, "revoked", "false"),
					resource.TestCheckNoResourceAttr(address, "secret_wo"),
					resource.TestCheckNoResourceAttr(address, "key"),
				)},
				{Config: config("Changed without new UUID", "var.credential"), PlanOnly: true,
					ExpectError: regexp.MustCompile("Credential configuration is immutable")},
				{Config: config("Workload", "null")},
			}
			if tc.droppedResponses == 3 {
				steps = append([]resource.TestStep{{Config: initial,
					ExpectError: regexp.MustCompile("Cannot register credential")}}, steps...)
			}

			resource.Test(t, resource.TestCase{
				IsUnitTest: true, ProtoV6ProviderFactories: factories(), PreventPostDestroyRefresh: true,
				Steps: steps,
				CheckDestroy: func(_ *terraform.State) error {
					mu.Lock()
					defer mu.Unlock()
					if !revoked {
						return fmt.Errorf("credential was not revoked")
					}
					return nil
				},
			})
			mu.Lock()
			defer mu.Unlock()
			if writes != 1 || createCalls != tc.droppedResponses+1 {
				t.Errorf("registration calls = %d, database writes = %d", createCalls, writes)
			}
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(log), secret) || strings.Contains(string(log), issuer) ||
				strings.Contains(string(log), hex.EncodeToString(digest[:])) {
				t.Error("Terraform trace logs exposed credential material")
			}
		})
	}
}
