package provider

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type webhookModel struct {
	ID            types.String `tfsdk:"id"`
	EnvironmentID types.String `tfsdk:"environment_id"`
	URL           types.String `tfsdk:"url"`
	Enabled       types.Bool   `tfsdk:"enabled"`
}

type eventWebhookModel struct {
	webhookModel
	Name       types.String `tfsdk:"name"`
	EventTypes types.Set    `tfsdk:"event_types"`
}

type webhookResource struct {
	client *client.Client
	event  bool
}

var (
	_ resource.ResourceWithConfigure   = &webhookResource{}
	_ resource.ResourceWithImportState = &webhookResource{}
)

func (r *webhookResource) kind() string {
	if r.event {
		return "eventWebhook"
	}
	return "snapshotWebhook"
}

func (r *webhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	suffix := "_snapshot_webhook"
	if r.event {
		suffix = "_event_webhook"
	}
	resp.TypeName = req.ProviderTypeName + suffix
}

func (r *webhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	description := "A Convex destination for snapshot.published events. An environment can have one destination. Destroy deletes the destination."
	if r.event {
		description = "An HTTPS endpoint for customer events. Destroy deletes the endpoint and cancels pending deliveries."
	}
	endpointURL := schema.StringAttribute{
		Required: true, Sensitive: true,
		Description: "HTTPS Convex callback URL ending in .convex.site without user information or a fragment. Terraform stores the complete URL in plan and state. Use a URL without credentials.",
		Validators:  []validator.String{webhookURLValidator{event: r.event}},
	}
	attributes := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed: true, Description: "Stable webhook ID.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"environment_id": schema.StringAttribute{
			Required: true, Description: "Owning environment ID. Changing it replaces the webhook.",
			Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"enabled": schema.BoolAttribute{
			Optional: true, Computed: true, Default: booldefault.StaticBool(true),
			Description: "Whether delivery is enabled. Defaults to true.",
		},
		"url": endpointURL,
	}
	if r.event {
		endpointURL.Description = "HTTPS destination without user information or a fragment. Changing the URL replaces the webhook. Terraform stores the complete URL in plan and state. Use a URL without credentials."
		endpointURL.PlanModifiers = []planmodifier.String{stringplanmodifier.RequiresReplace()}
		attributes["url"] = endpointURL
		attributes["name"] = schema.StringAttribute{
			Required: true, Description: "Display name. Changing the name replaces the webhook.",
			Validators:    []validator.String{webhookNameValidator{}},
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		}
		attributes["event_types"] = schema.SetAttribute{
			Required: true, ElementType: types.StringType,
			Description: "Set of event types to deliver. Use * to match all event types.",
			Validators: []validator.Set{
				setvalidator.SizeBetween(1, 100),
				setvalidator.ValueStringsAre(stringvalidator.RegexMatches(
					regexp.MustCompile(`^(\*|[a-zA-Z][a-zA-Z0-9_.-]{0,119})$`),
					"Use * or an event type of up to 120 characters starting with a letter and containing letters, digits, underscores, periods, or hyphens.",
				)),
			},
		}
	}
	resp.Schema = schema.Schema{Description: description + " To configure a new receiver, create the webhook with enabled set to false, rotate the signing secret in shipd, and save it in the receiver’s secret manager before enabling delivery. Terraform state contains the destination URL and omits signing secrets. Use shipd for test sends, delivery history, and redelivery.", Attributes: attributes}
}

func (r *webhookResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configureClient(req.ProviderData, &r.client, &resp.Diagnostics)
}

func (r *webhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	config := r.config(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.CreateWebhook(ctx, r.kind(), config)
	if err != nil {
		resp.Diagnostics.AddError("Cannot create webhook", err.Error())
		return
	}
	r.setState(ctx, &resp.State, &resp.Diagnostics, result)
}

func (r *webhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var id, environmentID types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("environment_id"), &environmentID)...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.ReadWebhook(ctx, r.kind(), environmentID.ValueString(), id.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read webhook", err.Error())
		return
	}
	r.setState(ctx, &resp.State, &resp.Diagnostics, result)
}

func (r *webhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	config := r.config(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.UpdateWebhook(ctx, r.kind(), config)
	if err != nil {
		resp.Diagnostics.AddError("Cannot update webhook", err.Error())
		return
	}
	r.setState(ctx, &resp.State, &resp.Diagnostics, result)
}

func (r *webhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id, environmentID types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("environment_id"), &environmentID)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteWebhook(ctx, r.kind(), environmentID.ValueString(), id.ValueString()); err != nil {
		resp.Diagnostics.AddError("Cannot delete webhook", err.Error())
	}
}

func (r *webhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	environmentID, id, ok := strings.Cut(req.ID, "/")
	if !ok || strings.TrimSpace(environmentID) == "" || strings.TrimSpace(id) == "" || strings.Contains(id, "/") {
		resp.Diagnostics.AddError("Invalid import ID", "Use environment_id/webhook_id.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("environment_id"), strings.TrimSpace(environmentID))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.TrimSpace(id))...)
}

func (r *webhookResource) config(ctx context.Context, plan tfsdk.Plan, diagnostics *diag.Diagnostics) client.Webhook {
	var common webhookModel
	var name string
	var eventTypes []string
	if r.event {
		var event eventWebhookModel
		diagnostics.Append(plan.Get(ctx, &event)...)
		if diagnostics.HasError() {
			return client.Webhook{}
		}
		diagnostics.Append(event.EventTypes.ElementsAs(ctx, &eventTypes, false)...)
		sort.Strings(eventTypes)
		common = event.webhookModel
		name = event.Name.ValueString()
	} else {
		diagnostics.Append(plan.Get(ctx, &common)...)
	}
	return client.Webhook{
		ID: common.ID.ValueString(), EnvironmentID: common.EnvironmentID.ValueString(),
		URL: common.URL.ValueString(), Enabled: common.Enabled.ValueBool(),
		Name: name, EventTypes: eventTypes,
	}
}

func (r *webhookResource) setState(ctx context.Context, state *tfsdk.State, diagnostics *diag.Diagnostics, result client.Webhook) {
	if result.ID == "" || result.EnvironmentID == "" || result.URL == "" ||
		(r.event && (result.Name == "" || len(result.EventTypes) == 0)) {
		diagnostics.AddError("Invalid webhook response", "shipd did not return the webhook configuration. Check the webhook in shipd before retrying.")
		return
	}
	common := webhookModel{
		ID: types.StringValue(result.ID), EnvironmentID: types.StringValue(result.EnvironmentID),
		URL: types.StringValue(result.URL), Enabled: types.BoolValue(result.Enabled),
	}
	if !r.event {
		diagnostics.Append(state.Set(ctx, common)...)
		return
	}
	eventTypes, setDiagnostics := types.SetValueFrom(ctx, types.StringType, result.EventTypes)
	diagnostics.Append(setDiagnostics...)
	if !diagnostics.HasError() {
		diagnostics.Append(state.Set(ctx, eventWebhookModel{
			webhookModel: common, Name: types.StringValue(result.Name), EventTypes: eventTypes,
		})...)
	}
}

type webhookURLValidator struct{ event bool }

func (v webhookURLValidator) Description(context.Context) string {
	if v.event {
		return "Use an HTTPS URL without user information, a fragment, or surrounding whitespace, up to 2,048 characters."
	}
	return "Use an HTTPS Convex callback URL ending in .convex.site without user information, a fragment, or surrounding whitespace, up to 2,048 characters."
}

func (v webhookURLValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v webhookURLValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	u, err := url.Parse(value)
	if err != nil || !webhookTextValid(value, 2048) || u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || strings.Contains(value, "#") ||
		(!v.event && !strings.HasSuffix(strings.ToLower(u.Hostname()), ".convex.site")) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid webhook URL", v.Description(ctx))
	}
}

type webhookNameValidator struct{}

func (webhookNameValidator) Description(context.Context) string {
	return "Use 1 to 120 UTF-16 code units without leading or trailing whitespace."
}

func (v webhookNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v webhookNameValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !req.ConfigValue.IsNull() && !req.ConfigValue.IsUnknown() && !webhookTextValid(req.ConfigValue.ValueString(), 120) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid webhook name", v.Description(ctx))
	}
}

func webhookTextValid(value string, maxLength int) bool {
	trimmed := strings.TrimFunc(value, func(r rune) bool {
		return unicode.Is(unicode.Zs, r) || strings.ContainsRune("\t\n\v\f\r\u2028\u2029\ufeff", r)
	})
	return value != "" && value == trimmed && len(utf16.Encode([]rune(value))) <= maxLength
}
