package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

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
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

type credentialResource struct {
	client *client.Client
	sdk    bool
}

var (
	_              resource.ResourceWithConfigure  = &credentialResource{}
	_              resource.ResourceWithModifyPlan = &credentialResource{}
	credentialUUID                                 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

func (r *credentialResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account_token"
	if r.sdk {
		resp.TypeName = req.ProviderTypeName + "_sdk_key"
	}
}

func (r *credentialResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Required: true, Description: "A stable, lowercase UUIDv4 for this credential generation. Use a fresh UUID and secret for every replacement.",
			Validators:    []validator.String{stringvalidator.RegexMatches(credentialUUID, "Use a lowercase UUIDv4.")},
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"organization_id": schema.StringAttribute{
			Required: true, Description: "Organization that owns the credential.",
		},
		"name": schema.StringAttribute{
			Required: true, Description: "The credential's display name.",
		},
		"project_id": schema.StringAttribute{
			Optional: true, Description: "Optional project binding. Omit for an organization-wide token.",
		},
		"environment_id": schema.StringAttribute{
			Optional: true, Description: "Optional environment binding within the project.",
			Validators: []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("project_id"))},
		},
		"expires_at": schema.StringAttribute{
			Required: true, Description: "Expiry timestamp in RFC3339 format.",
		},
		"secret_version": schema.StringAttribute{
			Required: true, Description: "Nonsecret identifier of the immutable secret-store version. A change requires a fresh UUID and credential.",
			Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
		},
		"secret_wo": schema.StringAttribute{
			Optional: true, Sensitive: true, WriteOnly: true,
			Description: "Externally generated shipd credential with an organization tag. Required on creation and replacement. Supply it from a secret store through an ephemeral variable. Requires Terraform 1.11 or later.",
		},
		"revoked": schema.BoolAttribute{
			Computed: true, Description: "Whether shipd has revoked the credential.",
		},
	}
	description := "Registers a workload token on an existing shipd service account. The token must expire. Destroy revokes it."
	if r.sdk {
		description = "Registers an environment-bound client or server SDK key. Destroy revokes it."
		attributes["project_id"] = schema.StringAttribute{Required: true, Description: "Project that owns the environment."}
		attributes["environment_id"] = schema.StringAttribute{Required: true, Description: "Environment to which the key is bound."}
		attributes["expires_at"] = schema.StringAttribute{Optional: true, Description: "Expiry timestamp in RFC3339 format. Omit for a non-expiring SDK key."}
		attributes["key_type"] = schema.StringAttribute{
			Required: true, Description: "Either client or server.",
			Validators: []validator.String{stringvalidator.OneOf("client", "server")},
		}
		attributes["scopes"] = schema.SetAttribute{
			Required: true, ElementType: types.StringType,
			Description: "Explicit SDK key scopes. Only server keys support identity:read.",
		}
	} else {
		attributes["service_account_id"] = schema.StringAttribute{Required: true, Description: "Service account that owns the workload token."}
		attributes["name"] = schema.StringAttribute{
			Required: true, Description: "The workload token's display name.",
			Validators: []validator.String{serviceAccountText{min: 1, max: 120}},
		}
		attributes["scopes"] = schema.MapAttribute{
			Required: true, ElementType: types.SetType{ElemType: types.StringType},
			Description: "Explicit resource-to-verb scope map. The issuer must hold every requested scope.",
		}
	}
	resp.Schema = schema.Schema{Description: description, Attributes: attributes}
}

func (r *credentialResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	configureClient(req.ProviderData, &r.client, &resp.Diagnostics)
}

func (r *credentialResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var previous, planned map[string]tftypes.Value
	if req.State.Raw.As(&previous) != nil || req.Plan.Raw.As(&planned) != nil {
		return
	}
	if !planned["id"].Equal(previous["id"]) {
		return
	}
	for name, value := range planned {
		if name != "secret_wo" && name != "revoked" && value.IsFullyKnown() && !value.Equal(previous[name]) {
			resp.Diagnostics.AddError("Credential configuration is immutable", "Set a new UUID in id and supply a fresh secret to replace this credential.")
			return
		}
	}
}

type credentialAttributes interface {
	GetAttribute(context.Context, path.Path, any) diag.Diagnostics
}

func (r *credentialResource) target(ctx context.Context, values credentialAttributes, diagnostics *diag.Diagnostics) map[string]any {
	input := make(map[string]any)
	fields := map[string]string{"organization_id": "orgId", "id": "tokenId", "service_account_id": "serviceAccountId"}
	if r.sdk {
		fields = map[string]string{"organization_id": "orgId", "id": "keyId", "project_id": "projectId", "environment_id": "environmentId"}
	}
	addCredentialStrings(ctx, values, fields, input, diagnostics)
	return input
}

func addCredentialStrings(ctx context.Context, values credentialAttributes, fields map[string]string, input map[string]any, diagnostics *diag.Diagnostics) {
	for attribute, field := range fields {
		var value types.String
		diagnostics.Append(values.GetAttribute(ctx, path.Root(attribute), &value)...)
		if !value.IsNull() && !value.IsUnknown() {
			input[field] = value.ValueString()
		}
	}
}

func (r *credentialResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	input := r.target(ctx, req.Plan, &resp.Diagnostics)
	operation := "serviceAccount/registerToken"
	prefix := "shipd_sa_"
	prefixLength := 24
	fields := map[string]string{"name": "name", "expires_at": "expiresAt", "project_id": "projectId", "environment_id": "environmentId"}
	if r.sdk {
		operation, prefixLength = "sdkKey/register", 28
		fields = map[string]string{"name": "name", "expires_at": "expiresAt", "key_type": "keyType"}
		input["id"] = input["keyId"]
		delete(input, "keyId")

		var scopes types.Set
		var requested []string
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("scopes"), &scopes)...)
		resp.Diagnostics.Append(scopes.ElementsAs(ctx, &requested, false)...)
		input["scopes"] = requested
	} else {
		input["id"] = input["tokenId"]
		delete(input, "tokenId")

		var scopes types.Map
		var requested map[string][]string
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("scopes"), &scopes)...)
		resp.Diagnostics.Append(scopes.ElementsAs(ctx, &requested, false)...)
		input["scopes"] = requested
	}
	addCredentialStrings(ctx, req.Plan, fields, input, &resp.Diagnostics)
	var secret types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("secret_wo"), &secret)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.sdk {
		keyType, _ := input["keyType"].(string)
		prefix = "shipd_sdk_" + keyType + "_"
	}
	if expiry, ok := input["expiresAt"].(string); ok {
		parsed, err := time.Parse(time.RFC3339, expiry)
		if err != nil || !parsed.After(time.Now()) {
			resp.Diagnostics.AddAttributeError(path.Root("expires_at"), "Invalid expiry", "Supply a future RFC3339 timestamp.")
			return
		}
	}
	organizationID, _ := input["orgId"].(string)
	keyPrefix, keyHash, ok := credentialMaterial(secret.ValueString(), prefix, prefixLength, organizationID)
	if !ok {
		resp.Diagnostics.AddAttributeError(path.Root("secret_wo"), "Invalid credential material", "Supply the shipd prefix and 32 random bytes encoded as unpadded base64url, followed by a dot and the organization tag described in the provider README.")
		return
	}
	input["keyPrefix"], input["keyHash"] = keyPrefix, keyHash

	// Registration is an atomic compare-or-create for this UUID and material.
	// It is safe to replay this exact body, including in a fresh apply.
	var registered struct {
		ID             string  `json:"id"`
		OrganizationID string  `json:"organizationId"`
		RevokedAt      *string `json:"revokedAt"`
	}
	if err := r.client.RPC(ctx, operation, input, &registered, false); err != nil {
		resp.Diagnostics.AddError("Cannot register credential", credentialDiagnostic(err))
		return
	}
	if registered.ID != input["id"] || registered.OrganizationID != input["orgId"] || registered.RevokedAt != nil {
		resp.Diagnostics.AddError("Invalid credential registration response", "The API did not confirm a live credential in the requested organization.")
		return
	}
	resp.State.Raw = req.Plan.Raw
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("revoked"), false)...)
}

func credentialMaterial(secret, prefix string, prefixLength int, organizationID string) (string, string, bool) {
	suffix, hasPrefix := strings.CutPrefix(secret, prefix)
	random, tag, hasTag := strings.Cut(suffix, ".")
	orgDigest := sha256.Sum256([]byte(organizationID))
	expectedTag := hex.EncodeToString(orgDigest[:16])
	material, err := base64.RawURLEncoding.Strict().DecodeString(random)
	if !hasPrefix || !hasTag || tag != expectedTag || err != nil || len(material) != 32 ||
		base64.RawURLEncoding.EncodeToString(material) != random {
		return "", "", false
	}
	digest := sha256.Sum256([]byte(secret))
	return secret[:prefixLength] + "." + tag, hex.EncodeToString(digest[:]), true
}

func (r *credentialResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	input := r.target(ctx, req.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	operation := "serviceAccount/getToken"
	if r.sdk {
		operation = "sdkKey/get"
	}
	var credential struct {
		RevokedAt *string `json:"revokedAt"`
	}
	if err := r.client.RPC(ctx, operation, input, &credential, false); err != nil {
		if r.isMissing(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Cannot read credential", credentialDiagnostic(err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("revoked"), credential.RevokedAt != nil)...)
}

func (r *credentialResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Credential configuration is immutable", "Set a new UUID in id and supply a fresh secret to replace this credential.")
}

func (r *credentialResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	input := r.target(ctx, req.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	operation := "serviceAccount/revokeToken"
	if r.sdk {
		operation = "sdkKey/revoke"
		delete(input, "projectId")
		delete(input, "environmentId")
	}
	if err := r.client.RPC(ctx, operation, input, nil, false); err != nil && !r.isMissing(err) {
		resp.Diagnostics.AddError("Cannot revoke credential", credentialDiagnostic(err))
	}
}

func (r *credentialResource) isMissing(err error) bool {
	message := "Service account token not found"
	if r.sdk {
		message = "SDK key not found"
	}
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound &&
		apiErr.Code == "not_found" && apiErr.Message == message
}

func credentialDiagnostic(err error) string {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "Verify that the provider uses a live service account token with the required scopes and bindings for this organization."
		case http.StatusNotFound:
			return "Verify the credential's parent resources exist and that the shipd API supports this operation."
		case http.StatusConflict:
			return "Confirm the parent is enabled and the UUID, credential material, and metadata match the original registration. Load the original secret-store version to recover. Use a fresh UUID and secret for a new credential."
		case http.StatusUnprocessableEntity:
			return "Verify the organization, resource IDs, credential type, scopes, name and expiration."
		}
	}
	return "The request did not complete. Re-run with the same UUID, secret-store version and metadata to reconcile a registration."
}
