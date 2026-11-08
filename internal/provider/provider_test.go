package provider

import (
	"encoding/json"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){"shipd": providerserver.NewProtocol6WithError(New("test")())}
}
func lifecycleConfig(url, key, slug, name string) string {
	environmentName, environmentSlug, environmentType, production := "Development", "dev", "development", false
	if name == "Updated" {
		environmentName, environmentSlug, environmentType, production = "Canary", "canary", "canary", true
	}
	return fmt.Sprintf(`
provider "shipd" {
 api_url = %q
 api_key = %q
 organization_id = "org-test"
}
resource "shipd_project" "test" {
 name = %q
 slug = %q
}
resource "shipd_environment" "test" {
 project_id = shipd_project.test.id
 name = %q
 slug = %q
 type = %q
 is_production = %t
}
data "shipd_project" "by_slug" { slug = shipd_project.test.slug }
data "shipd_project" "by_id" { id = shipd_project.test.id }
data "shipd_environment" "by_slug" {
 project_id = shipd_project.test.id
 slug = shipd_environment.test.slug
}
data "shipd_environment" "by_id" {
 project_id = shipd_project.test.id
 id = shipd_environment.test.id
}
`, url, key, name, slug, environmentName, environmentSlug, environmentType, production)
}
func importEnvironment(s *terraform.State) (string, error) {
	root := s.RootModule().Resources
	return " " + root["shipd_project.test"].Primary.ID + " / " + root["shipd_environment.test"].Primary.ID + " ", nil
}
func lifecycleSteps(first, updated string) []resource.TestStep {
	return []resource.TestStep{
		{Config: first, Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("shipd_environment.test", "type", "development"), resource.TestCheckResourceAttr("shipd_environment.test", "is_production", "false"), resource.TestCheckResourceAttrPair("data.shipd_project.by_slug", "id", "shipd_project.test", "id"), resource.TestCheckResourceAttrPair("data.shipd_environment.by_id", "id", "shipd_environment.test", "id"))},
		{ResourceName: "shipd_project.test", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: func(s *terraform.State) (string, error) {
			return " " + s.RootModule().Resources["shipd_project.test"].Primary.ID + " ", nil
		}},
		{ResourceName: "shipd_environment.test", ImportState: true, ImportStateIdFunc: importEnvironment, ImportStateVerify: true},
		{Config: updated, Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr("shipd_project.test", "name", "Updated"),
			resource.TestCheckResourceAttr("shipd_environment.test", "slug", "canary"),
			resource.TestCheckResourceAttr("shipd_environment.test", "type", "canary"),
			resource.TestCheckResourceAttr("shipd_environment.test", "is_production", "true"),
		)},
		{ResourceName: "shipd_environment.test", ImportState: true, ImportStateIdFunc: importEnvironment, ImportStateVerify: true},
	}
}
func TestTerraformLifecycle(t *testing.T) {
	f := newFixture(t)
	first := lifecycleConfig(f.server.URL, "test-token", "terraform-test", "Original")
	updated := lifecycleConfig(f.server.URL, "test-token", "terraform-test", "Updated")
	steps := lifecycleSteps(first, updated)
	steps = append(steps,
		resource.TestStep{PreConfig: func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, p := range f.projects {
				p["name"] = "External edit"
				p["version"] = p["version"].(int) + 1
			}
		}, Config: updated, Check: resource.TestCheckResourceAttr("shipd_project.test", "name", "Updated")},
		resource.TestStep{PreConfig: func() { f.mu.Lock(); defer f.mu.Unlock(); clear(f.environments) }, Config: updated, Check: resource.TestCheckResourceAttr("shipd_environment.test", "slug", "canary")},
	)
	resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: factories(), PreventPostDestroyRefresh: true, Steps: steps, CheckDestroy: func(_ *terraform.State) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.projects) != 0 || len(f.environments) != 0 {
			return fmt.Errorf("objects were not archived")
		}
		return nil
	}})
}
func TestTerraformOrganizationGuard(t *testing.T) {
	f := newFixture(t)
	config := strings.ReplaceAll(lifecycleConfig(f.server.URL, "test-token", "guard-test", "Original"), `organization_id = "org-test"`, `organization_id = "wrong-org"`)
	resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: factories(), Steps: []resource.TestStep{{Config: config, PlanOnly: true, ExpectError: regexp.MustCompile("Organization mismatch")}}})
}
func TestTerraformLookupRequiresOneSelector(t *testing.T) {
	f := newFixture(t)
	for name, selector := range map[string]string{"neither": "", "both": `id = "p"` + "\n" + `slug = "slug"`} {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: factories(), Steps: []resource.TestStep{{Config: fmt.Sprintf("provider \"shipd\" {\napi_url = %q\napi_key = \"test-token\"\n}\ndata \"shipd_project\" \"test\" {\n%s\n}\n", f.server.URL, selector), PlanOnly: true, ExpectError: regexp.MustCompile("Invalid Attribute Combination")}}})
		})
	}
}
func TestAccTerraformLifecycle(t *testing.T) {
	url, key, org := os.Getenv("SHIPD_API_URL"), os.Getenv("SHIPD_API_KEY"), os.Getenv("SHIPD_ORGANIZATION_ID")
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set")
	}
	if url == "" || key == "" || org == "" {
		t.Fatal("isolated API URL, token, and organization ID are required")
	}
	slug := fmt.Sprintf("tf-acceptance-%d", time.Now().UnixNano())
	config := func(name string) string {
		return strings.ReplaceAll(lifecycleConfig(url, key, slug, name), `organization_id = "org-test"`, fmt.Sprintf("organization_id = %q", org))
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories(), PreventPostDestroyRefresh: true, Steps: lifecycleSteps(config("Original"), config("Updated"))})
}

type fixture struct {
	mu                     sync.Mutex
	server                 *httptest.Server
	projects, environments map[string]map[string]any
	next                   int
	conflictOperation      string
	conflictCalls          int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{projects: map[string]map[string]any{}, environments: map[string]map[string]any{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}
func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("x-api-key") != "test-token" {
		w.WriteHeader(401)
		return
	}
	var input map[string]any
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(422)
		return
	}
	operation := strings.TrimPrefix(r.URL.Path, "/api/v1/rpc/")
	parts := strings.Split(operation, "/")
	write := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	if operation == "me" {
		write(map[string]any{"organization": map[string]string{"id": "org-test"}})
		return
	}
	if len(parts) != 2 {
		w.WriteHeader(404)
		return
	}
	collection := f.projects
	idField := "projectId"
	if parts[0] == "environment" {
		collection = f.environments
		idField = "environmentId"
	}
	// Match the API's atomic production exclusivity check under the fixture lock.
	if parts[0] == "environment" && input["isProduction"] == true && (parts[1] == "create" || parts[1] == "updateWithoutHandoff") {
		projectID := input["projectId"]
		if current := f.environments[fmt.Sprint(input["environmentId"])]; current != nil {
			projectID = current["projectId"]
		}
		for _, existing := range f.environments {
			if existing["projectId"] == projectID && existing["isProduction"] == true && existing["id"] != input["environmentId"] {
				w.WriteHeader(http.StatusConflict)
				return
			}
		}
	}
	switch parts[1] {
	case "list":
		items := []map[string]any{}
		for _, item := range collection {
			if parts[0] != "environment" || item["projectId"] == input["projectId"] {
				items = append(items, item)
			}
		}
		write(items)
	case "create":
		f.next++
		id := fmt.Sprintf("id-%d", f.next)
		item := map[string]any{"id": id, "version": 1}
		for k, v := range input {
			item[k] = v
		}
		if parts[0] == "project" {
			item["organizationId"] = "org-test"
		}
		collection[id] = item
		write(item)
	case "update", "updateWithoutHandoff":
		item := collection[input[idField].(string)]
		if item == nil {
			w.WriteHeader(404)
			return
		}
		if operation == f.conflictOperation {
			f.conflictCalls++
			item["version"] = item["version"].(int) + 1
		}
		if input["expectedVersion"] != float64(item["version"].(int)) {
			w.WriteHeader(409)
			return
		}
		for k, v := range input {
			if k != "expectedVersion" && k != idField {
				item[k] = v
			}
		}
		item["version"] = item["version"].(int) + 1
		write(item)
	case "archive":
		delete(collection, input[idField].(string))
		write(map[string]bool{"success": true})
	default:
		w.WriteHeader(404)
	}
}

func TestTerraformEmptyOrganizationGuard(t *testing.T) {
	f := newFixture(t)
	config := strings.ReplaceAll(lifecycleConfig(f.server.URL, "test-token", "guard-test", "Original"), `organization_id = "org-test"`, `organization_id = ""`)
	resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: factories(), Steps: []resource.TestStep{{Config: config, PlanOnly: true, ExpectError: regexp.MustCompile("Invalid organization_id")}}})
}

func TestTerraformConcurrentUpdate(t *testing.T) {
	for _, operation := range []string{"project/update", "environment/updateWithoutHandoff"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			first := lifecycleConfig(f.server.URL, "test-token", "conflict-test", "Original")
			updated := strings.Replace(first, `name = "Original"`, `name = "Changed"`, 1)
			if strings.HasPrefix(operation, "environment/") {
				updated = strings.Replace(first, `name = "Development"`, `name = "Changed"`, 1)
			}
			resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: factories(), PreventPostDestroyRefresh: true, Steps: []resource.TestStep{
				{Config: first},
				{PreConfig: func() { f.mu.Lock(); defer f.mu.Unlock(); f.conflictOperation = operation }, Config: updated, ExpectError: regexp.MustCompile("shipd API 409")},
				{PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					if f.conflictCalls != 1 {
						t.Errorf("conflicting update attempted %d times", f.conflictCalls)
					}
					f.conflictOperation = ""
				}, Config: updated},
			}})
		})
	}
}

func TestTerraformCreateSecondProductionEnvironment(t *testing.T) {
	f := newFixture(t)
	config := lifecycleConfig(f.server.URL, "test-token", "production-test", "Updated")
	second := config + `
resource "shipd_environment" "second" {
 project_id = shipd_project.test.id
 name = "Second"
 slug = "second"
 is_production = true
}
`
	resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: factories(), PreventPostDestroyRefresh: true, Steps: []resource.TestStep{
		{Config: config},
		{Config: second, ExpectError: regexp.MustCompile("Production environment already exists")},
		{Config: config},
	}})
}
