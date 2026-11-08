package provider

import (
	"context"
	"errors"
	"fmt"
	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"strings"
)

type projectModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	Name           types.String `tfsdk:"name"`
	Slug           types.String `tfsdk:"slug"`
	Version        types.Int64  `tfsdk:"version"`
}

func projectState(p client.Project) projectModel {
	return projectModel{ID: types.StringValue(p.ID), OrganizationID: types.StringValue(p.OrganizationID), Name: types.StringValue(p.Name), Slug: types.StringValue(p.Slug), Version: types.Int64Value(p.Version)}
}

type projectResource struct{ client *client.Client }

var _ resource.ResourceWithImportState = &projectResource{}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}
func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "A shipd project. Destroy archives the project; recovery and slug reservation last seven days. Use lifecycle.prevent_destroy to protect important projects.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "Stable project ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"organization_id": schema.StringAttribute{Computed: true, Description: "Organization that owns this project.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"name":            schema.StringAttribute{Required: true, Description: "Project display name.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
		"slug":            schema.StringAttribute{Required: true, Description: "Project slug; at least three characters.", Validators: []validator.String{stringvalidator.LengthAtLeast(3)}},
		"version":         schema.Int64Attribute{Computed: true, Description: "API version used for optimistic concurrency checks."},
	}}
}
func configureClient(data any, target **client.Client, diagnostics *diag.Diagnostics) {
	if data == nil {
		return
	}
	c, ok := data.(*client.Client)
	if !ok {
		diagnostics.AddError("Unexpected provider configuration", fmt.Sprintf("Expected shipd client, received %T", data))
		return
	}
	*target = c
}
func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configureClient(req.ProviderData, &r.client, &resp.Diagnostics)
}
func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var p client.Project
	if err := r.client.RPC(ctx, "project/create", map[string]any{"name": data.Name.ValueString(), "slug": data.Slug.ValueString()}, &p, true); err != nil {
		resp.Diagnostics.AddError("Cannot create project", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, projectState(p))...)
}
func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.Project(ctx, data.ID.ValueString(), "")
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read project", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, projectState(p))...)
}
func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, prior projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var p client.Project
	if err := r.client.RPC(ctx, "project/update", map[string]any{"projectId": prior.ID.ValueString(), "name": data.Name.ValueString(), "slug": data.Slug.ValueString(), "expectedVersion": prior.Version.ValueInt64()}, &p, true); err != nil {
		resp.Diagnostics.AddError("Cannot update project", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, projectState(p))...)
}
func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.RPC(ctx, "project/archive", map[string]any{"projectId": data.ID.ValueString()}, nil, true)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Cannot archive project", err.Error())
	}
}
func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Use the project ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.TrimSpace(req.ID))...)
}
