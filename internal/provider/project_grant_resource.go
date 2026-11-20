package provider

import (
	"context"
	"regexp"
	"strings"

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
)

type projectGrantModel struct {
	ID             types.String `tfsdk:"id"`
	OrganizationID types.String `tfsdk:"organization_id"`
	ProjectID      types.String `tfsdk:"project_id"`
	PrincipalType  types.String `tfsdk:"principal_type"`
	PrincipalID    types.String `tfsdk:"principal_id"`
	Role           types.String `tfsdk:"role"`
}

type projectGrantResource struct{ client *client.Client }

var (
	_           resource.ResourceWithConfigure      = &projectGrantResource{}
	_           resource.ResourceWithImportState    = &projectGrantResource{}
	_           resource.ResourceWithValidateConfig = &projectGrantResource{}
	grantIDPart                                     = regexp.MustCompile(`^[^/\s]+$`)
)

func (r *projectGrantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_access_grant"
}

func (r *projectGrantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages one direct member grant or team assignment in a shipd project. Use a service-account token with project_access:read for import and refresh and project_access:manage for writes. Each project and principal pair should have a single Terraform resource. Removing the final team assignment restores organization-wide project access under shipd's normal authorization rules.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, Description: "Grant ID in the format PROJECT_ID/PRINCIPAL_TYPE/PRINCIPAL_ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"organization_id": schema.StringAttribute{
				Computed: true, Description: "Organization that owns the project.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": schema.StringAttribute{
				Required: true, Description: "Project ID.",
				Validators:    []validator.String{stringvalidator.RegexMatches(grantIDPart, "must be a nonempty ID without slashes or whitespace")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"principal_type": schema.StringAttribute{
				Required: true, Description: "Principal type, either member or team.",
				Validators:    []validator.String{stringvalidator.OneOf("member", "team")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"principal_id": schema.StringAttribute{
				Required: true, Description: "Existing organization membership ID for a member, or team ID for a team.",
				Validators:    []validator.String{stringvalidator.RegexMatches(grantIDPart, "must be a nonempty ID without slashes or whitespace")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"role": schema.StringAttribute{
				Required: true, Description: "Project role, either viewer or editor. Use inherited for a team assignment whose role follows shipd's organization-role inheritance rules.",
				Validators: []validator.String{stringvalidator.OneOf("viewer", "editor", "inherited")},
			},
		},
	}
}

func (r *projectGrantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configureClient(req.ProviderData, &r.client, &resp.Diagnostics)
}

func (r *projectGrantResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config projectGrantModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if config.PrincipalType.ValueString() == "member" && config.Role.ValueString() == "inherited" {
		resp.Diagnostics.AddAttributeError(path.Root("role"), "Invalid direct grant role", "Member grants require the viewer or editor role.")
	}
}

func (r *projectGrantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.write(ctx, &plan, nil, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *projectGrantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org, grant, err := r.client.GetProjectGrant(ctx, state.target())
	switch {
	case err != nil && !client.IsProjectGrantProjectNotFound(err):
		resp.Diagnostics.AddError("Cannot read project access grant", err.Error())
	case grant == nil:
		resp.State.RemoveResource(ctx)
	default:
		state.setResult(org, grantRole(grant))
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	}
}

func (r *projectGrantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.OrganizationID = state.OrganizationID
	if r.write(ctx, &plan, state.grant(), &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
}

func (r *projectGrantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, grant, err := r.client.ChangeProjectGrant(ctx, state.target(), state.grant(), nil)
	switch {
	case err != nil && !client.IsProjectGrantProjectNotFound(err):
		resp.Diagnostics.AddError("Cannot delete project access grant", err.Error())
	case grant != nil:
		resp.Diagnostics.AddError("Cannot delete project access grant", "The API still reports an assignment.")
	}
}

func (r *projectGrantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	parts := strings.Split(id, "/")
	if len(parts) != 3 || !grantIDPart.MatchString(parts[0]) || (parts[1] != "member" && parts[1] != "team") || !grantIDPart.MatchString(parts[2]) {
		resp.Diagnostics.AddError("Invalid import ID", "Use PROJECT_ID/member/MEMBER_ID or PROJECT_ID/team/TEAM_ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("principal_type"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("principal_id"), parts[2])...)
}

func (r *projectGrantResource) write(ctx context.Context, plan *projectGrantModel, expected *client.ProjectGrant, diagnostics *diag.Diagnostics) bool {
	org, grant, err := r.client.ChangeProjectGrant(ctx, plan.target(), expected, plan.grant())
	if err != nil {
		diagnostics.AddError("Cannot write project access grant", err.Error())
		return false
	}
	if grant == nil || grantRole(grant) != plan.Role.ValueString() {
		diagnostics.AddError("Cannot write project access grant", "The API returned a different grant than requested.")
		return false
	}
	plan.setResult(org, grantRole(grant))
	return true
}

func (m projectGrantModel) target() client.ProjectGrantTarget {
	return client.ProjectGrantTarget{
		ProjectID:      m.ProjectID.ValueString(),
		OrganizationID: m.OrganizationID.ValueString(),
		Kind:           m.PrincipalType.ValueString(),
		PrincipalID:    m.PrincipalID.ValueString(),
	}
}

func (m projectGrantModel) grant() *client.ProjectGrant {
	role := m.Role.ValueString()
	if role == "inherited" {
		return &client.ProjectGrant{}
	}
	return &client.ProjectGrant{Role: &role}
}

func grantRole(grant *client.ProjectGrant) string {
	if grant.Role == nil {
		return "inherited"
	}
	return *grant.Role
}

func (m *projectGrantModel) setResult(organizationID, role string) {
	m.ID = types.StringValue(strings.Join([]string{m.ProjectID.ValueString(), m.PrincipalType.ValueString(), m.PrincipalID.ValueString()}, "/"))
	m.OrganizationID = types.StringValue(organizationID)
	m.Role = types.StringValue(role)
}
