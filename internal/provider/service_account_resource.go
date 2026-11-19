package provider

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type serviceAccountModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	Enabled        types.Bool   `tfsdk:"enabled"`
}

func serviceAccountState(account client.ServiceAccount) serviceAccountModel {
	return serviceAccountModel{
		ID:             types.StringValue(account.ID),
		OrganizationID: types.StringValue(account.OrganizationID),
		Name:           types.StringValue(account.Name),
		Description:    types.StringPointerValue(account.Description),
		Enabled:        types.BoolValue(account.DisabledAt == nil),
	}
}

type serviceAccountResource struct{ client *client.Client }

var (
	_ resource.ResourceWithConfigure   = &serviceAccountResource{}
	_ resource.ResourceWithImportState = &serviceAccountResource{}
)

func (r *serviceAccountResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account"
}

func (r *serviceAccountResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An organization-level shipd automation identity. Destroy permanently deletes the account and all its tokens.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, Description: "Stable service account ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"organization_id": schema.StringAttribute{
				Computed: true, Description: "Organization that owns the service account.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true, Description: "Name of the service account, unique in the organization.",
				Validators: []validator.String{serviceAccountText{min: 1, max: 120}},
			},
			"description": schema.StringAttribute{
				Optional: true, Description: "Description of the service account.",
				Validators: []validator.String{serviceAccountText{max: 2000}},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether tokens can authenticate. Re-enabling restores access for tokens that have not expired or been revoked. Defaults to true.",
			},
		},
	}
}

type serviceAccountText struct{ min, max int }

func (v serviceAccountText) Description(context.Context) string {
	return fmt.Sprintf("Must be %d to %d UTF-16 code units without leading or trailing whitespace.", v.min, v.max)
}

func (v serviceAccountText) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v serviceAccountText) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	// Match JavaScript String.trim() and length, which the API uses for these fields.
	trimmed := strings.TrimFunc(value, func(r rune) bool {
		return unicode.Is(unicode.Zs, r) || strings.ContainsRune("\t\n\v\f\r\u2028\u2029\ufeff", r)
	})
	length := len(utf16.Encode([]rune(value)))
	if value != trimmed || length < v.min || length > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid service account text", v.Description(ctx))
	}
}

func (r *serviceAccountResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configureClient(req.ProviderData, &r.client, &resp.Diagnostics)
}

func (r *serviceAccountResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var identity client.Identity
	if err := r.client.RPC(ctx, "me", struct{}{}, &identity, false); err != nil {
		resp.Diagnostics.AddError("Cannot resolve service account organization", err.Error())
		return
	}
	input := map[string]any{
		"orgId":       identity.Organization.ID,
		"name":        plan.Name.ValueString(),
		"description": plan.Description.ValueStringPointer(),
	}
	if !plan.Enabled.ValueBool() {
		input["enabled"] = false
	}
	var account client.ServiceAccount
	if err := r.client.RPC(ctx, "serviceAccount/create", input, &account, true); err != nil {
		resp.Diagnostics.AddError("Cannot create service account", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceAccountState(account))...)
}

func (r *serviceAccountResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var account client.ServiceAccount
	err := r.client.RPC(ctx, "serviceAccount/get", state.target(), &account, false)
	if client.IsServiceAccountNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read service account", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceAccountState(account))...)
}

func (r *serviceAccountResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	input := state.target()
	nameChanged, descriptionChanged := !plan.Name.Equal(state.Name), !plan.Description.Equal(state.Description)
	if nameChanged {
		input["name"] = plan.Name.ValueString()
	}
	if descriptionChanged {
		input["description"] = plan.Description.ValueStringPointer()
	}
	operation := "serviceAccount/update"
	if !plan.Enabled.Equal(state.Enabled) {
		switch {
		case nameChanged || descriptionChanged:
			input["enabled"] = plan.Enabled.ValueBool()
		case plan.Enabled.ValueBool():
			operation = "serviceAccount/enable"
		default:
			operation = "serviceAccount/disable"
		}
	}

	var account client.ServiceAccount
	if err := r.client.RPC(ctx, operation, input, &account, true); err != nil {
		resp.Diagnostics.AddError("Cannot update service account", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceAccountState(account))...)
}

func (r *serviceAccountResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.RPC(ctx, "serviceAccount/delete", state.target(), nil, true)
	if err != nil && !client.IsServiceAccountNotFound(err) {
		resp.Diagnostics.AddError("Cannot delete service account", err.Error())
	}
}

func (r *serviceAccountResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Use the service account ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (s serviceAccountModel) target() map[string]any {
	input := map[string]any{"serviceAccountId": s.ID.ValueString()}
	if !s.OrganizationID.IsNull() {
		input["orgId"] = s.OrganizationID.ValueString()
	}
	return input
}
