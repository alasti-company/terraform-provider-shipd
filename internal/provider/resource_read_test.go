package provider

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestResourceReadDistinguishesMissingObjectFromFailedList(t *testing.T) {
	for _, environment := range []bool{false, true} {
		for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusForbidden} {
			t.Run(fmt.Sprintf("environment=%t/status=%d", environment, status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = w.Write([]byte(`{"data":[]}`))
					}
				}))
				defer server.Close()
				c, err := client.New(server.URL, "secret")
				if err != nil {
					t.Fatal(err)
				}
				var r resource.Resource = &projectResource{client: c}
				var model any = projectState(client.Project{ID: "project", Name: "Project", Slug: "project", Version: 1})
				if environment {
					r = &environmentResource{client: c}
					model = environmentState(client.Environment{ID: "environment", ProjectID: "project", Name: "Env", Slug: "env", Version: 1})
				}
				ctx := context.Background()
				var schema resource.SchemaResponse
				r.Schema(ctx, resource.SchemaRequest{}, &schema)
				state := tfsdk.State{Schema: schema.Schema}
				if diags := state.Set(ctx, model); diags.HasError() {
					t.Fatal(diags)
				}
				response := resource.ReadResponse{State: state}
				r.Read(ctx, resource.ReadRequest{State: state}, &response)
				if status == http.StatusOK {
					if response.Diagnostics.HasError() || !response.State.Raw.IsNull() {
						t.Fatalf("missing object not removed: %v", response.Diagnostics)
					}
				} else {
					if !response.Diagnostics.HasError() {
						t.Fatal("failed list did not return a diagnostic")
					}
					var id types.String
					if diags := response.State.GetAttribute(ctx, path.Root("id"), &id); diags.HasError() || id.ValueString() == "" {
						t.Fatalf("state lost after failed list: %v", diags)
					}
				}
			})
		}
	}
}
