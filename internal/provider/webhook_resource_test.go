package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccTerraformWebhooks(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set")
	}
	apiURL := os.Getenv("SHIPD_API_URL")
	token := os.Getenv("SHIPD_API_KEY")
	organizationID := os.Getenv("SHIPD_ORGANIZATION_ID")
	environmentID := os.Getenv("SHIPD_TERRAFORM_ENVIRONMENT_ID")
	if apiURL == "" || token == "" || organizationID == "" || environmentID == "" {
		t.Fatal("isolated API URL, token, organization ID, and environment ID are required")
	}

	const (
		snapshotAddress = "shipd_snapshot_webhook.test"
		eventAddress    = "shipd_event_webhook.test"
		initialURL      = "https://original.convex.site/shipd/webhook"
		updatedURL      = "https://updated.convex.site/shipd/webhook"
	)
	config := func(snapshotURL string, enabled bool, eventTypes string) string {
		return fmt.Sprintf(`
resource "shipd_snapshot_webhook" "test" {
  environment_id = %q
  url            = %q
  enabled        = %t
}
resource "shipd_event_webhook" "test" {
  environment_id = %q
  name           = "Invoice events"
  url            = "https://receiver.example/webhooks"
  enabled        = %t
  event_types    = %s
}
`, environmentID, snapshotURL, enabled, environmentID, enabled, eventTypes)
	}
	initial := config(initialURL, false, `["invoice.paid", "invoice.voided"]`)
	updated := config(updatedURL, true, `["*"]`)
	c, err := client.New(apiURL, token)
	if err != nil {
		t.Fatal(err)
	}
	var snapshotID, eventID string
	importID := func(id *string) func(*terraform.State) (string, error) {
		return func(*terraform.State) (string, error) {
			return environmentID + "/" + *id, nil
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories:  factories(),
		PreventPostDestroyRefresh: true,
		Steps: []resource.TestStep{
			{Config: initial, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(snapshotAddress, "enabled", "false"),
				resource.TestCheckResourceAttr(eventAddress, "enabled", "false"),
				resource.TestCheckResourceAttr(eventAddress, "event_types.#", "2"),
				resource.TestCheckNoResourceAttr(snapshotAddress, "signing_secret"),
				resource.TestCheckNoResourceAttr(eventAddress, "signing_secret"),
				func(state *terraform.State) error {
					snapshotID = state.RootModule().Resources[snapshotAddress].Primary.ID
					eventID = state.RootModule().Resources[eventAddress].Primary.ID
					return nil
				},
			)},
			{ResourceName: snapshotAddress, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: importID(&snapshotID)},
			{ResourceName: eventAddress, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: importID(&eventID)},
			{Config: updated, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(snapshotAddress, "url", updatedURL),
				resource.TestCheckResourceAttr(snapshotAddress, "enabled", "true"),
				resource.TestCheckResourceAttr(eventAddress, "enabled", "true"),
				resource.TestCheckTypeSetElemAttr(eventAddress, "event_types.*", "*"),
			)},
			{Config: updated, PreConfig: func() {
				ctx := context.Background()
				if _, err := c.UpdateWebhook(ctx, "snapshotWebhook", client.Webhook{
					ID: snapshotID, EnvironmentID: environmentID, URL: initialURL, Enabled: false,
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := c.UpdateWebhook(ctx, "eventWebhook", client.Webhook{
					ID: eventID, EnvironmentID: environmentID, EventTypes: []string{"invoice.paid"}, Enabled: false,
				}); err != nil {
					t.Fatal(err)
				}
			}, PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: updated, Check: func(state *terraform.State) error {
				root := state.RootModule().Resources
				if root[snapshotAddress].Primary.ID != snapshotID || root[eventAddress].Primary.ID != eventID {
					return fmt.Errorf("drift reconciliation replaced a webhook")
				}
				return nil
			}},
			{Config: updated},
		},
		CheckDestroy: func(_ *terraform.State) error {
			for kind, id := range map[string]string{"snapshotWebhook": snapshotID, "eventWebhook": eventID} {
				_, err := c.ReadWebhook(context.Background(), kind, environmentID, id)
				if !errors.Is(err, client.ErrNotFound) {
					return fmt.Errorf("%s still exists or could not be read: %v", kind, err)
				}
			}
			return nil
		},
	})
}

func TestSnapshotWebhookURLValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		url   string
		valid bool
	}{
		{"Convex URL", "https://example.convex.site/shipd/webhook", true},
		{"userinfo", "https://user:secret@example.convex.site/shipd/webhook", false},
		{"fragment", "https://example.convex.site/shipd/webhook#fragment", false},
		{"non-Convex URL", "https://receiver.example/shipd/webhook", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response validator.StringResponse
			webhookURLValidator{}.ValidateString(context.Background(), validator.StringRequest{
				Path: path.Root("url"), ConfigValue: types.StringValue(tc.url),
			}, &response)
			if response.Diagnostics.HasError() == tc.valid {
				t.Fatalf("expected valid=%t, got %v", tc.valid, response.Diagnostics)
			}
		})
	}
}
