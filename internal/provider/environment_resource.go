package provider

import (
	"context"
	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"strings"
)

type environmentModel struct {
	ID           types.String `tfsdk:"id"`
	ProjectID    types.String `tfsdk:"project_id"`
	Name         types.String `tfsdk:"name"`
	Slug         types.String `tfsdk:"slug"`
	Type         types.String `tfsdk:"type"`
	IsProduction types.Bool   `tfsdk:"is_production"`
	Version      types.Int64  `tfsdk:"version"`
}

func environmentState(e client.Environment) environmentModel {
	return environmentModel{ID: types.StringValue(e.ID), ProjectID: types.StringValue(e.ProjectID), Name: types.StringValue(e.Name), Slug: types.StringValue(e.Slug), Type: types.StringValue(e.Type), IsProduction: types.BoolValue(e.IsProduction), Version: types.Int64Value(e.Version)}
}

type environmentResource struct{ client *client.Client }

var _ resource.ResourceWithImportState = &environmentResource{}

func (r *environmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}
func (r *environmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "A shipd environment. Destroy archives it for seven-day recovery. Demote the current production environment before promoting another; implicit demotion is rejected.", Attributes: map[string]schema.Attribute{
		"id":            schema.StringAttribute{Computed: true, Description: "Stable environment ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"project_id":    schema.StringAttribute{Required: true, Description: "Owning project ID. Changing it replaces the environment.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"name":          schema.StringAttribute{Required: true, Description: "Environment display name.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
		"slug":          schema.StringAttribute{Required: true, Description: "Slug unique within the project.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
		"type":          schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("development"), Description: "Free-form classification. Defaults to development; does not control permissions.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
		"is_production": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Whether this environment is production. Defaults to false. Only one production environment is allowed per project."},
		"version":       schema.Int64Attribute{Computed: true, Description: "API version used for optimistic concurrency checks."},
	}}
}
func (r *environmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configureClient(req.ProviderData, &r.client, &resp.Diagnostics)
}
func (r *environmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var e client.Environment
	if err := r.client.RPC(ctx, "environment/create", map[string]any{"projectId": data.ProjectID.ValueString(), "name": data.Name.ValueString(), "slug": data.Slug.ValueString(), "type": data.Type.ValueString(), "isProduction": data.IsProduction.ValueBool()}, &e, true); err != nil {
		resp.Diagnostics.AddError("Cannot create environment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, environmentState(e))...)
}
func (r *environmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, err := r.client.Environment(ctx, data.ProjectID.ValueString(), data.ID.ValueString(), "")
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read environment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, environmentState(e))...)
}
func (r *environmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, prior environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Early feedback; the API also enforces this under the project lock.
	if data.IsProduction.ValueBool() && !prior.IsProduction.ValueBool() {
		items, err := r.client.Environments(ctx, prior.ProjectID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Cannot check production environment", err.Error())
			return
		}
		for _, e := range items {
			if e.IsProduction && e.ID != prior.ID.ValueString() {
				resp.Diagnostics.AddError("Production environment already exists", "Demote the current production environment in a separate apply before promoting this environment.")
				return
			}
		}
	}
	var e client.Environment
	if err := r.client.RPC(ctx, "environment/updateWithoutHandoff", map[string]any{"environmentId": prior.ID.ValueString(), "name": data.Name.ValueString(), "slug": data.Slug.ValueString(), "type": data.Type.ValueString(), "isProduction": data.IsProduction.ValueBool(), "expectedVersion": prior.Version.ValueInt64()}, &e, true); err != nil {
		resp.Diagnostics.AddError("Cannot update environment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, environmentState(e))...)
}
func (r *environmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.RPC(ctx, "environment/archive", map[string]any{"environmentId": data.ID.ValueString()}, nil, true)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Cannot archive environment", err.Error())
	}
}
func (r *environmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Use project_id/environment_id.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
