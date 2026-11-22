package provider

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf16"

	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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

type auditDestinationResource struct{ client *client.Client }

const defaultAuditS3Prefix = "shipd-audit"

var (
	_ resource.ResourceWithConfigure      = &auditDestinationResource{}
	_ resource.ResourceWithImportState    = &auditDestinationResource{}
	_ resource.ResourceWithValidateConfig = &auditDestinationResource{}
	_ resource.ResourceWithModifyPlan     = &auditDestinationResource{}
)

type auditDestinationModel struct {
	ID                types.String `tfsdk:"id"`
	Type              types.String `tfsdk:"type"`
	Name              types.String `tfsdk:"name"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	URL               types.String `tfsdk:"url"`
	Events            types.Set    `tfsdk:"events"`
	Format            types.String `tfsdk:"format"`
	Endpoint          types.String `tfsdk:"endpoint"`
	Region            types.String `tfsdk:"region"`
	Bucket            types.String `tfsdk:"bucket"`
	Prefix            types.String `tfsdk:"prefix"`
	AccessKeyID       types.String `tfsdk:"access_key_id"`
	CredentialWO      types.String `tfsdk:"credential_wo"`
	CredentialVersion types.String `tfsdk:"credential_version"`
}

func (r *auditDestinationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_audit_destination"
}

func (r *auditDestinationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	optionalString := func(description string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true, Description: description,
			Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
		}
	}
	address := func(description string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true, Sensitive: true, Description: description,
		}
	}
	accessKeyID := optionalString("S3 access key ID. Terraform stores this identifier in plan and state as sensitive.")
	accessKeyID.Sensitive = true
	resp.Schema = schema.Schema{
		Description: "Manages one webhook, SIEM, or S3-compatible audit export destination. Destroy removes this destination. " +
			"Use an organization-wide service-account token with organization:update and the audit log entitlement. " +
			"Use shipd to view delivery history, send tests, and generate one-off exports.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, Description: "Stable audit destination ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"type": schema.StringAttribute{
				Required: true, Description: "Destination type. Choose webhook, siem, or s3. Changing the type replaces the destination.",
				Validators:    []validator.String{stringvalidator.OneOf("webhook", "siem", "s3")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true, Description: "Display name, 1 to 80 UTF-16 code units.",
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether shipd streams audit events to this destination. Defaults to true.",
			},
			"url": address("Required for webhook and SIEM. Use an absolute URL without credentials, a query, or a fragment. Terraform stores this URL in plan and state as sensitive."),
			"events": schema.SetAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "Webhook audit actions to deliver. Omit to deliver all audit actions.",
				Validators:  []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1))},
			},
			"format": schema.StringAttribute{
				Optional: true, Description: "Required for SIEM. Choose json, splunk_hec, datadog, axiom, or better_stack.",
				Validators: []validator.String{stringvalidator.OneOf("json", "splunk_hec", "datadog", "axiom", "better_stack")},
			},
			"endpoint":      address("Required for S3. Use an absolute S3-compatible endpoint without credentials, a query, or a fragment. Terraform stores this URL in plan and state as sensitive."),
			"region":        optionalString("Required for S3. Region used to sign requests."),
			"bucket":        optionalString("Required for S3. Name of the bucket that receives audit events."),
			"prefix":        optionalString("S3 object key prefix. Omit to use shipd-audit."),
			"access_key_id": accessKeyID,
			"credential_wo": schema.StringAttribute{
				Optional: true, Sensitive: true, WriteOnly: true,
				Description: "Webhook signing secret (at least 16 characters), SIEM token, or S3 secret access key. Supply with credential_version on creation or rotation from an ephemeral input. Requires Terraform 1.11 or later.",
			},
			"credential_version": optionalString("Nonsecret version of the credential. Set it for creation or change it to rotate the credential in place. Omit when adopting an existing credential through import."),
		},
	}
}

func (r *auditDestinationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configureClient(req.ProviderData, &r.client, &resp.Diagnostics)
}

func (r *auditDestinationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config auditDestinationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateAuditDestinationConfig(config, &resp.Diagnostics)
	if !config.CredentialWO.IsNull() && !config.CredentialWO.IsUnknown() && config.CredentialVersion.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("credential_version"), "Missing credential version", "Set a nonsecret version when supplying credential_wo.")
	}
}

func validateAuditDestinationConfig(config auditDestinationModel, diagnostics *diag.Diagnostics) {
	if !config.Name.IsNull() && !config.Name.IsUnknown() && !webhookTextValid(config.Name.ValueString(), 80) {
		diagnostics.AddAttributeError(path.Root("name"), "Invalid audit destination name", "Use 1 to 80 UTF-16 code units without leading or trailing whitespace.")
	}
	for _, address := range []struct {
		name  string
		value types.String
	}{{"url", config.URL}, {"endpoint", config.Endpoint}} {
		if !address.value.IsNull() && !address.value.IsUnknown() && !validAuditDestinationURL(address.value.ValueString()) {
			diagnostics.AddAttributeError(path.Root(address.name), "Invalid audit destination URL", "Use an absolute HTTP(S) URL without credentials, a query, or a fragment.")
		}
	}
	kind := config.Type.ValueString()
	if kind == "" {
		return
	}
	for _, field := range []struct {
		name     string
		allowed  bool
		required bool
		value    attr.Value
	}{
		{"url", kind != "s3", kind != "s3", config.URL},
		{"events", kind == "webhook", false, config.Events},
		{"format", kind == "siem", kind == "siem", config.Format},
		{"endpoint", kind == "s3", kind == "s3", config.Endpoint},
		{"region", kind == "s3", kind == "s3", config.Region},
		{"bucket", kind == "s3", kind == "s3", config.Bucket},
		{"prefix", kind == "s3", false, config.Prefix},
		{"access_key_id", kind == "s3", kind == "s3", config.AccessKeyID},
	} {
		if field.value.IsUnknown() {
			continue
		}
		if field.required && field.value.IsNull() {
			diagnostics.AddAttributeError(path.Root(field.name), "Missing destination setting", "This argument is required for type "+kind+".")
		}
		if !field.allowed && !field.value.IsNull() {
			diagnostics.AddAttributeError(path.Root(field.name), "Incompatible destination setting", "Omit this argument for type "+kind+".")
		}
	}
}

func (r *auditDestinationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan, prior auditDestinationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	}
	if resp.Diagnostics.HasError() || plan.Type.IsUnknown() || plan.CredentialVersion.IsUnknown() {
		return
	}
	if !plan.Type.Equal(prior.Type) || destinationCredentialChanged(plan, prior) {
		var secret types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("credential_wo"), &secret)...)
		if !resp.Diagnostics.HasError() {
			validateDestinationCredential(plan, secret, false, &resp.Diagnostics)
		}
	} else if !plan.AccessKeyID.IsUnknown() && !plan.AccessKeyID.Equal(prior.AccessKeyID) {
		addS3RotationDiagnostic(&resp.Diagnostics)
	}
}

func (r *auditDestinationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan auditDestinationModel
	var secret types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("credential_wo"), &secret)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateAuditDestinationConfig(plan, &resp.Diagnostics)
	validateDestinationCredential(plan, secret, true, &resp.Diagnostics)
	config := plan.destinationConfig(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.CreateAuditDestination(ctx, plan.Type.ValueString(), plan.Name.ValueString(), plan.Enabled.ValueBool(), config, secret.ValueString())
	if err != nil {
		detail := auditDiagnostic(err, "organization:update", false)
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.Status >= http.StatusInternalServerError || apiErr.Status == http.StatusConflict {
			detail += " If the destination exists in shipd, import its ID before applying again."
		}
		resp.Diagnostics.AddError("Cannot create audit destination", detail)
		return
	}
	var responseDiagnostics diag.Diagnostics
	setAuditDestinationState(ctx, &resp.State, plan, result, &responseDiagnostics)
	if !responseDiagnostics.HasError() {
		resp.Diagnostics.Append(responseDiagnostics...)
		return
	}
	// Recover the created object by ID without repeating the credential write.
	if result.ID != "" {
		if fresh, err := r.client.AuditDestination(ctx, result.ID); err == nil {
			var readDiagnostics diag.Diagnostics
			setAuditDestinationState(ctx, &resp.State, plan, fresh, &readDiagnostics)
			if !readDiagnostics.HasError() {
				resp.Diagnostics.Append(readDiagnostics...)
				return
			}
		}
	}
	resp.Diagnostics.Append(responseDiagnostics...)
	if result.ID == "" || result.Type != "" && result.Type != plan.Type.ValueString() {
		resp.Diagnostics.AddWarning("Audit destination may have been created", "Check the destination list in shipd and import its ID if it exists before applying again.")
		return
	}
	plan.ID = types.StringValue(result.ID)
	plan.CredentialWO = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	resp.Diagnostics.AddWarning("Audit destination ID captured", "Check the destination in shipd and refresh Terraform state before retrying. A failed create can leave the resource marked for replacement.")
}

func (r *auditDestinationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state auditDestinationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.AuditDestination(ctx, state.ID.ValueString())
	switch {
	case errors.Is(err, client.ErrNotFound):
		resp.State.RemoveResource(ctx)
	case err != nil:
		resp.Diagnostics.AddError("Cannot read audit destination", auditDiagnostic(err, "organization:update", false))
	default:
		setAuditDestinationState(ctx, &resp.State, state, result, &resp.Diagnostics)
	}
}

func (r *auditDestinationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior auditDestinationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateAuditDestinationConfig(plan, &resp.Diagnostics)

	var secret types.String
	rotate := destinationCredentialChanged(plan, prior)
	if rotate {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("credential_wo"), &secret)...)
		validateDestinationCredential(plan, secret, true, &resp.Diagnostics)
	} else if !plan.AccessKeyID.Equal(prior.AccessKeyID) {
		addS3RotationDiagnostic(&resp.Diagnostics)
	}
	var config *client.AuditDestinationConfig
	if rotate || !plan.sameDestinationConfig(prior) {
		value := plan.destinationConfig(ctx, &resp.Diagnostics)
		switch plan.Type.ValueString() {
		case "webhook":
			if plan.Events.Equal(prior.Events) {
				value.Events = nil
			} else if plan.Events.IsNull() {
				// An empty patch list clears a previously installed filter.
				value.Events = &[]string{}
			}
		case "s3":
			if plan.Prefix.Equal(prior.Prefix) {
				value.Prefix = nil
			} else if plan.Prefix.IsNull() {
				// The API merges updates, so resetting a custom prefix needs an explicit value.
				prefix := defaultAuditS3Prefix
				value.Prefix = &prefix
			}
		}
		config = &value
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var name *string
	if !plan.Name.Equal(prior.Name) {
		name = plan.Name.ValueStringPointer()
	}
	var enabled *bool
	if !plan.Enabled.Equal(prior.Enabled) {
		enabled = plan.Enabled.ValueBoolPointer()
	}
	if config == nil && name == nil && enabled == nil {
		// Removing a credential version relinquishes rotation without a remote change.
		prior.CredentialVersion = plan.CredentialVersion
		resp.Diagnostics.Append(resp.State.Set(ctx, prior)...)
		return
	}
	result, err := r.client.UpdateAuditDestination(ctx, prior.ID.ValueString(), plan.Type.ValueString(), name, enabled, config, secret.ValueString())
	if err != nil {
		detail := auditDiagnostic(err, "organization:update", false)
		var apiErr *client.APIError
		if rotate && (!errors.As(err, &apiErr) || apiErr.Status >= http.StatusInternalServerError) {
			detail += " Verify the active credential with the receiver. If needed, retry with the same credential version and secret."
		}
		resp.Diagnostics.AddError("Cannot update audit destination", detail)
		return
	}
	setAuditDestinationState(ctx, &resp.State, plan, result, &resp.Diagnostics)
}

func (r *auditDestinationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAuditDestination(ctx, id.ValueString()); err != nil {
		resp.Diagnostics.AddError("Cannot delete audit destination", auditDiagnostic(err, "organization:update", false))
	}
}

func (r *auditDestinationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		resp.Diagnostics.AddError("Invalid audit destination import ID", "Supply the audit destination ID from shipd.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func destinationCredentialChanged(plan, prior auditDestinationModel) bool {
	return !plan.CredentialVersion.IsNull() && !plan.CredentialVersion.Equal(prior.CredentialVersion)
}

func addS3RotationDiagnostic(diagnostics *diag.Diagnostics) {
	diagnostics.AddAttributeError(path.Root("access_key_id"), "Missing S3 credential rotation", "Change credential_version and supply the corresponding S3 secret access key in credential_wo when changing access_key_id.")
}

func validateDestinationCredential(plan auditDestinationModel, secret types.String, applying bool, diagnostics *diag.Diagnostics) {
	if plan.CredentialVersion.IsNull() {
		diagnostics.AddAttributeError(path.Root("credential_version"), "Missing credential version", "Set a nonsecret version for the credential used to create or rotate this destination.")
	}
	if secret.IsUnknown() && !applying {
		return
	}
	value := secret.ValueString()
	if strings.TrimSpace(value) == "" || value == "••••" {
		diagnostics.AddAttributeError(path.Root("credential_wo"), "Missing destination credential", "Supply a credential from an ephemeral input for creation or rotation.")
	} else if plan.Type.ValueString() == "webhook" && len(utf16.Encode([]rune(value))) < 16 {
		diagnostics.AddAttributeError(path.Root("credential_wo"), "Invalid webhook signing secret", "Use a signing secret of at least 16 characters.")
	}
}

func (m auditDestinationModel) sameDestinationConfig(prior auditDestinationModel) bool {
	return m.URL.Equal(prior.URL) && m.Events.Equal(prior.Events) && m.Format.Equal(prior.Format) &&
		m.Endpoint.Equal(prior.Endpoint) && m.Region.Equal(prior.Region) && m.Bucket.Equal(prior.Bucket) &&
		m.Prefix.Equal(prior.Prefix) && m.AccessKeyID.Equal(prior.AccessKeyID)
}

func (m auditDestinationModel) destinationConfig(ctx context.Context, diagnostics *diag.Diagnostics) client.AuditDestinationConfig {
	switch m.Type.ValueString() {
	case "webhook":
		config := client.AuditDestinationConfig{URL: m.URL.ValueString()}
		if !m.Events.IsNull() {
			var events []string
			diagnostics.Append(m.Events.ElementsAs(ctx, &events, false)...)
			config.Events = &events
		}
		return config
	case "siem":
		return client.AuditDestinationConfig{URL: m.URL.ValueString(), Format: m.Format.ValueString()}
	default: // S3; type is validated by the schema.
		return client.AuditDestinationConfig{
			Endpoint: m.Endpoint.ValueString(), Region: m.Region.ValueString(), Bucket: m.Bucket.ValueString(),
			Prefix: m.Prefix.ValueStringPointer(), AccessKeyID: m.AccessKeyID.ValueString(),
		}
	}
}

func setAuditDestinationState(ctx context.Context, target *tfsdk.State, model auditDestinationModel, result client.AuditDestination, diagnostics *diag.Diagnostics) {
	if result.ID == "" || result.Name == "" || result.Enabled == nil ||
		!model.Type.IsNull() && result.Type != model.Type.ValueString() {
		diagnostics.AddError("Invalid audit destination response", "The API did not return a complete destination of the expected type.")
		return
	}
	state := auditDestinationModel{
		ID: types.StringValue(result.ID), Type: types.StringValue(result.Type),
		Name: types.StringValue(result.Name), Enabled: types.BoolPointerValue(result.Enabled),
		CredentialVersion: model.CredentialVersion, Events: types.SetNull(types.StringType),
	}
	address := result.Config.URL
	switch result.Type {
	case "webhook":
		if result.Config.Events != nil && len(*result.Config.Events) != 0 {
			events, ds := types.SetValueFrom(ctx, types.StringType, *result.Config.Events)
			diagnostics.Append(ds...)
			state.Events = events
		}
		state.URL = types.StringValue(address)
	case "siem":
		if result.Config.Format == "" {
			diagnostics.AddError("Invalid audit destination response", "The API did not return a SIEM format.")
			return
		}
		state.URL, state.Format = types.StringValue(address), types.StringValue(result.Config.Format)
	case "s3":
		if result.Config.Region == "" || result.Config.Bucket == "" || result.Config.AccessKeyID == "" {
			diagnostics.AddError("Invalid audit destination response", "The API did not return the S3 region, bucket, and access key ID.")
			return
		}
		address = result.Config.Endpoint
		state.Endpoint, state.Region = types.StringValue(address), types.StringValue(result.Config.Region)
		state.Bucket, state.AccessKeyID = types.StringValue(result.Config.Bucket), types.StringValue(result.Config.AccessKeyID)
		if prefix := result.Config.Prefix; prefix != nil && *prefix != "" &&
			(*prefix != defaultAuditS3Prefix || !model.Prefix.IsNull()) {
			state.Prefix = types.StringValue(*prefix)
		}
	default:
		diagnostics.AddError("Unsupported audit destination", "Use a webhook, SIEM, or S3 audit destination.")
		return
	}
	if !validAuditDestinationURL(address) {
		diagnostics.AddError("Unsupported audit destination URL", "Use an absolute HTTP(S) URL without credentials, a query, or a fragment in shipd before importing or refreshing this destination.")
		return
	}
	if !diagnostics.HasError() {
		diagnostics.Append(target.Set(ctx, state)...)
	}
}

func validAuditDestinationURL(value string) bool {
	address, err := url.Parse(value)
	return err == nil && value == strings.TrimSpace(value) && !strings.Contains(value, "#") &&
		(address.Scheme == "https" || address.Scheme == "http") && address.Hostname() != "" &&
		address.User == nil && !address.ForceQuery && address.RawQuery == ""
}
