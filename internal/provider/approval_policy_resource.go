package provider

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type approvalPolicyResource struct{ client *client.Client }

var (
	_ resource.ResourceWithConfigure   = &approvalPolicyResource{}
	_ resource.ResourceWithImportState = &approvalPolicyResource{}
)

type approvalPolicyModel struct {
	ID                    types.String  `tfsdk:"id"`
	Name                  types.String  `tfsdk:"name"`
	Description           types.String  `tfsdk:"description"`
	Enabled               types.Bool    `tfsdk:"enabled"`
	Priority              types.Int64   `tfsdk:"priority"`
	ProjectIDs            types.Set     `tfsdk:"project_ids"`
	EnvironmentIDs        types.Set     `tfsdk:"environment_ids"`
	EnvironmentProduction types.Bool    `tfsdk:"environment_production"`
	MinRolloutIncrease    types.Float64 `tfsdk:"min_rollout_increase"`
	RequiredApprovals     types.Int64   `tfsdk:"required_approvals"`
	ReviewerRoles         types.Set     `tfsdk:"reviewer_roles"`
	ReviewerTeamIDs       types.Set     `tfsdk:"reviewer_team_ids"`
	AllowSelfApproval     types.Bool    `tfsdk:"allow_self_approval"`
	UpdatedAt             types.String  `tfsdk:"updated_at"`
}

func (r *approvalPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_approval_policy"
}

func (r *approvalPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	matchIDs := schema.SetAttribute{
		ElementType: types.StringType, Optional: true, Computed: true,
		Default:    setdefault.StaticValue(types.SetValueMust(types.StringType, nil)),
		Validators: []validator.Set{setvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1))},
	}
	projectIDs := matchIDs
	projectIDs.Description = "Project IDs to match. An empty set matches any project in the organization."
	environmentIDs := matchIDs
	environmentIDs.Description = "Environment IDs to match. An empty set matches any environment. When project_ids is set, every environment must belong to a selected project."
	teamIDs := matchIDs
	teamIDs.Description = "Additional teams from which eligible organization reviewers may be selected. Defaults to an empty set."
	teamIDs.Validators = append(teamIDs.Validators, setvalidator.SizeAtMost(100))
	resp.Schema = schema.Schema{
		Description: "Configures one shipd approval policy using an API that supports Terraform approval policies. " +
			"Use an organization-wide service-account token with approval_policy:read for import and refresh and approval_policy:update for writes. " +
			"Creation and updates require Enterprise. An enabled policy can be strengthened in place. " +
			"Weakening, disabling, or removing an enabled policy requires an organization administrator or owner in shipd. " +
			"Destroy permanently removes a disabled policy. Policy changes make pending requests stale. Reviewers decide newly submitted requests in shipd.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, Description: "Stable approval policy ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true, Description: "Policy display name.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 120)},
			},
			"description": schema.StringAttribute{
				Optional: true, Description: "Policy description.",
				Validators: []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether the policy is enforced. Defaults to true. Create a disabled policy to stage changes.",
			},
			"priority": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(100),
				Description: "Evaluation priority from 0 to 10000. For flag reviews, the first enabled match governs and lower numbers run first. Defaults to 100.",
				Validators:  []validator.Int64{int64validator.Between(0, 10000)},
			},
			"project_ids":     projectIDs,
			"environment_ids": environmentIDs,
			"environment_production": schema.BoolAttribute{
				Optional: true, Description: "Match production when true or non-production when false. Omit to match either.",
			},
			"min_rollout_increase": schema.Float64Attribute{
				Optional: true, Description: "Minimum rollout increase in percentage points from 0 to 100. Omit for no threshold. The threshold applies to flag reviews.",
				Validators: []validator.Float64{float64validator.Between(0, 100)},
			},
			"required_approvals": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				Description: "Number of distinct human approvals required, from 1 to 20. Defaults to 1.",
				Validators:  []validator.Int64{int64validator.Between(1, 20)},
			},
			"reviewer_roles": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				Default:     setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{types.StringValue("admin"), types.StringValue("owner")})),
				Description: "Eligible organization roles. Choose admin, owner, or both. Defaults to both.",
				Validators:  []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(stringvalidator.OneOf("admin", "owner"))},
			},
			"reviewer_team_ids": teamIDs,
			"allow_self_approval": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Whether an otherwise eligible human requester can review their own flag request. Defaults to false.",
			},
			"updated_at": schema.StringAttribute{
				Computed: true, Description: "API timestamp used to detect concurrent changes.",
			},
		},
	}
}

func (r *approvalPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configureClient(req.ProviderData, &r.client, &resp.Diagnostics)
}

func (r *approvalPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan approvalPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	input := plan.config(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.CreateApprovalPolicy(ctx, input)
	if err != nil {
		detail := approvalPolicyError(err)
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.Status >= http.StatusInternalServerError || apiErr.Status == http.StatusTooManyRequests ||
			(apiErr.Status == http.StatusConflict && apiErr.Code == "conflict") {
			detail += "\nIf the policy was created in shipd, import its ID before applying again."
		}
		resp.Diagnostics.AddError("Cannot create approval policy", detail)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, approvalPolicyState(ctx, result, &resp.Diagnostics))...)
}

func (r *approvalPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state approvalPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.ApprovalPolicy(ctx, state.ID.ValueString())
	switch {
	case errors.Is(err, client.ErrNotFound):
		resp.State.RemoveResource(ctx)
	case err != nil:
		resp.Diagnostics.AddError("Cannot read approval policy", approvalPolicyError(err))
	default:
		resp.Diagnostics.Append(resp.State.Set(ctx, approvalPolicyState(ctx, result, &resp.Diagnostics))...)
	}
}

func (r *approvalPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior approvalPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	input := plan.config(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.UpdateApprovalPolicy(ctx, prior.ID.ValueString(), prior.UpdatedAt.ValueString(), input)
	if err != nil {
		resp.Diagnostics.AddError("Cannot update approval policy", approvalPolicyError(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, approvalPolicyState(ctx, result, &resp.Diagnostics))...)
}

func (r *approvalPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state approvalPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteApprovalPolicy(ctx, state.ID.ValueString(), state.UpdatedAt.ValueString()); err != nil {
		resp.Diagnostics.AddError("Cannot delete approval policy", approvalPolicyError(err))
	}
}

func (r *approvalPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		resp.Diagnostics.AddError("Invalid approval policy import ID", "Provide the policy ID from shipd.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (m approvalPolicyModel) config(ctx context.Context, diagnostics *diag.Diagnostics) client.ApprovalPolicyConfig {
	config := client.ApprovalPolicyConfig{
		Name: m.Name.ValueString(), Description: m.Description.ValueStringPointer(),
		Enabled: m.Enabled.ValueBool(), Priority: m.Priority.ValueInt64(),
		Match: client.ApprovalPolicyMatch{
			EnvironmentProduction: m.EnvironmentProduction.ValueBoolPointer(),
			MinRolloutIncrease:    m.MinRolloutIncrease.ValueFloat64Pointer(),
		},
		RequiredApprovals: m.RequiredApprovals.ValueInt64(),
		AllowSelfApproval: m.AllowSelfApproval.ValueBool(),
	}
	diagnostics.Append(m.ProjectIDs.ElementsAs(ctx, &config.Match.ProjectIDs, false)...)
	diagnostics.Append(m.EnvironmentIDs.ElementsAs(ctx, &config.Match.EnvironmentIDs, false)...)
	diagnostics.Append(m.ReviewerRoles.ElementsAs(ctx, &config.ReviewerRoles, false)...)
	diagnostics.Append(m.ReviewerTeamIDs.ElementsAs(ctx, &config.ReviewerTeamIDs, false)...)
	if config.ReviewerTeamIDs == nil {
		config.ReviewerTeamIDs = []string{}
	}
	return config
}

func approvalPolicyState(ctx context.Context, policy client.ApprovalPolicy, diagnostics *diag.Diagnostics) approvalPolicyModel {
	stringSet := func(values []string) types.Set {
		// In Terraform, an omitted match list and an empty one both mean unrestricted.
		values = append([]string{}, values...)
		slices.Sort(values)
		set, ds := types.SetValueFrom(ctx, types.StringType, slices.Compact(values))
		diagnostics.Append(ds...)
		return set
	}
	return approvalPolicyModel{
		ID:   types.StringValue(policy.ID),
		Name: types.StringValue(policy.Name), Description: types.StringPointerValue(policy.Description),
		Enabled: types.BoolValue(policy.Enabled), Priority: types.Int64Value(policy.Priority),
		ProjectIDs: stringSet(policy.Match.ProjectIDs), EnvironmentIDs: stringSet(policy.Match.EnvironmentIDs),
		EnvironmentProduction: types.BoolPointerValue(policy.Match.EnvironmentProduction),
		MinRolloutIncrease:    types.Float64PointerValue(policy.Match.MinRolloutIncrease),
		RequiredApprovals:     types.Int64Value(policy.RequiredApprovals),
		ReviewerRoles:         stringSet(policy.ReviewerRoles), ReviewerTeamIDs: stringSet(policy.ReviewerTeamIDs),
		AllowSelfApproval: types.BoolValue(policy.AllowSelfApproval),
		UpdatedAt:         types.StringValue(policy.UpdatedAt),
	}
}

func approvalPolicyError(err error) string {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == http.StatusUnauthorized ||
			apiErr.Status == http.StatusForbidden && (apiErr.Message == "You do not have permission to perform this action" ||
				apiErr.Message == "Only user sessions can read approval workflows"):
			return err.Error() + "\nUse an organization-wide service-account token with approval_policy:read for refresh and approval_policy:update for changes. Verify that the shipd API supports Terraform approval policies."
		case apiErr.Status == http.StatusNotFound && apiErr.Message != "Approval policy not found":
			return err.Error() + "\nVerify that the shipd API supports Terraform approval policies."
		}
	}
	return err.Error()
}
