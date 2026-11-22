package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type auditSettingsResource struct{ client *client.Client }

var (
	_ resource.ResourceWithConfigure   = &auditSettingsResource{}
	_ resource.ResourceWithImportState = &auditSettingsResource{}
)

type auditSettingsModel struct {
	ID                   types.String `tfsdk:"id"`
	OrganizationID       types.String `tfsdk:"organization_id"`
	RetentionDays        types.Int64  `tfsdk:"retention_days"`
	DefaultRetentionDays types.Int64  `tfsdk:"default_retention_days"`
	MinimumRetentionDays types.Int64  `tfsdk:"minimum_retention_days"`
	LegalHold            types.Bool   `tfsdk:"legal_hold"`
}

func (r *auditSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_audit_settings"
}

func (r *auditSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the requested audit retention for one organization. Use one Terraform address per organization. " +
			"Use an organization-wide service-account token with audit:read for import and refresh and organization:update for changes. " +
			"Destroy relinquishes management and leaves the current retention policy in effect.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, Description: "The organization ID used to identify this singleton.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"organization_id": schema.StringAttribute{
				Required: true, Description: "Organization ID. It must match the credential's organization.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"retention_days": schema.Int64Attribute{
				Required: true, Description: "Requested retention in days, from 7 to 2555, subject to the plan limit and protected minimum. Set to 0 to keep events indefinitely under an unlimited entitlement.",
				Validators: []validator.Int64{int64validator.Any(int64validator.OneOf(0), int64validator.Between(7, 2555))},
			},
			"default_retention_days": schema.Int64Attribute{
				Computed: true, Description: "Default requested retention before the plan limit and protected minimum are applied.",
			},
			"minimum_retention_days": schema.Int64Attribute{
				Computed: true, Description: "Protected organization minimum in days, or 0 when no minimum is configured.",
			},
			"legal_hold": schema.BoolAttribute{
				Computed: true, Description: "Whether a legal hold prevents expiry and changes to retention.",
			},
		},
	}
}

func (r *auditSettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configureClient(req.ProviderData, &r.client, &resp.Diagnostics)
}

func (r *auditSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan auditSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !resp.Diagnostics.HasError() {
		r.reconcile(ctx, plan, &resp.State, &resp.Diagnostics)
	}
}

func (r *auditSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state auditSettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !r.verifyOrganization(ctx, state.OrganizationID.ValueString(), &resp.Diagnostics) {
		return
	}
	settings, err := r.client.AuditSettings(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read audit settings", auditDiagnostic(err, "audit:read", true))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, auditRetentionState(state.OrganizationID, settings))...)
}

func (r *auditSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan auditSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !resp.Diagnostics.HasError() {
		r.reconcile(ctx, plan, &resp.State, &resp.Diagnostics)
	}
}

func (r *auditSettingsResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
	// Ownership is limited to the requested value. There is no remote deletion.
}

func (r *auditSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		resp.Diagnostics.AddError("Invalid audit settings import ID", "Supply the shipd organization ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (r *auditSettingsResource) reconcile(ctx context.Context, plan auditSettingsModel, state *tfsdk.State, diagnostics *diag.Diagnostics) {
	if !r.verifyOrganization(ctx, plan.OrganizationID.ValueString(), diagnostics) {
		return
	}
	settings, err := r.client.AuditSettings(ctx)
	if err != nil {
		diagnostics.AddError("Cannot read audit settings", auditDiagnostic(err, "audit:read", true))
		return
	}
	if !auditRetentionState(plan.OrganizationID, settings).RetentionDays.Equal(plan.RetentionDays) {
		days := plan.RetentionDays.ValueInt64Pointer()
		if plan.RetentionDays.ValueInt64() == 0 {
			days = nil
		}
		settings, err = r.client.UpdateAuditSettings(ctx, days)
		if err != nil {
			diagnostics.AddError("Cannot update audit retention", auditDiagnostic(err, "organization:update", true))
			return
		}
	}
	diagnostics.Append(state.Set(ctx, auditRetentionState(plan.OrganizationID, settings))...)
}

func (r *auditSettingsResource) verifyOrganization(ctx context.Context, expected string, diagnostics *diag.Diagnostics) bool {
	var identity client.Identity
	if err := r.client.RPC(ctx, "me", struct{}{}, &identity, false); err != nil {
		diagnostics.AddError("Cannot verify audit settings organization", auditDiagnostic(err, "audit:read", true))
		return false
	}
	if identity.Organization.ID == "" || identity.Organization.ID != expected {
		diagnostics.AddError("Audit settings organization mismatch", "Use an organization-wide service-account token for the organization specified in organization_id or the import ID.")
		return false
	}
	return true
}

func auditRetentionState(id types.String, settings client.AuditSettings) auditSettingsModel {
	days := int64(0)
	if settings.RetentionDays != nil {
		days = *settings.RetentionDays
	}
	return auditSettingsModel{
		ID: id, OrganizationID: id, RetentionDays: types.Int64Value(days),
		DefaultRetentionDays: types.Int64Value(settings.DefaultRetentionDays),
		MinimumRetentionDays: types.Int64Value(settings.MinimumRetentionDays),
		LegalHold:            types.BoolValue(settings.LegalHold),
	}
}

// The response can echo user-supplied data. Classify known API errors without
// writing the body, transport error, or credential fields into diagnostics.
func auditDiagnostic(err error, scope string, settings bool) string {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == "quota_exceeded":
			if settings {
				return "The requested retention exceeds the organization's plan limit. Set retention_days within the granted limit or change the plan entitlement."
			}
			return "The organization has reached its audit destination quota. Remove an unused destination or increase the plan entitlement."
		case apiErr.Status == http.StatusForbidden && apiErr.Message == "Audit log is available on the Teams plan.":
			return "This organization needs the audit log entitlement, available on Teams and Enterprise."
		case apiErr.Status == http.StatusForbidden && apiErr.Message == "Unlimited audit retention is not included in your plan.":
			return "Keeping events indefinitely requires unlimited audit retention. Set retention_days to a finite value within your plan limit or change the entitlement."
		case apiErr.Status == http.StatusForbidden && apiErr.Message == "Retention is protected by an organization hold or minimum retention policy.":
			return "Check legal_hold and minimum_retention_days. Choose a permitted retention period or contact shipd support about the hold or protected minimum."
		case apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden:
			return "Use a live organization-wide service-account token with " + scope + " and verify it belongs to the intended organization."
		case apiErr.Status == http.StatusNotFound:
			return "Verify that the shipd API supports this audit operation and that the requested object exists."
		case apiErr.Status == http.StatusUnprocessableEntity:
			if settings {
				return "Verify that retention_days is within the supported range."
			}
			return "Verify the destination configuration, credential, and URL. The endpoint must resolve and satisfy the shipd API's destination network policy."
		case apiErr.Status == http.StatusConflict:
			return "Refresh the Terraform plan and check the current configuration in shipd before retrying."
		case apiErr.Status == http.StatusTooManyRequests:
			return "The API rate limit was reached. Wait before retrying."
		}
	}
	return "The shipd API did not confirm the operation. Check the API endpoint and current configuration in shipd."
}
