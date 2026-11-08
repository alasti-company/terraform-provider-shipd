package provider

import (
	"context"
	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"os"
)

type shipdProvider struct{ version string }
type providerModel struct {
	APIKey         types.String `tfsdk:"api_key"`
	APIURL         types.String `tfsdk:"api_url"`
	OrganizationID types.String `tfsdk:"organization_id"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &shipdProvider{version: version} }
}
func (p *shipdProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "shipd"
	resp.Version = p.version
}
func (p *shipdProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Provision shipd projects and environments using the control-plane API.", Attributes: map[string]schema.Attribute{
		"api_key":         schema.StringAttribute{Optional: true, Sensitive: true, Description: "Management key or service-account token. Defaults to SHIPD_API_KEY; use an organization-wide service-account token to create projects."},
		"api_url":         schema.StringAttribute{Optional: true, Description: "API base URL. HTTPS is required except for localhost or loopback IP addresses. Defaults to SHIPD_API_URL or https://api.useshipd.com."},
		"organization_id": schema.StringAttribute{Optional: true, Description: "Optional guard against using credentials for the wrong organization. Does not switch organizations. Defaults to SHIPD_ORGANIZATION_ID."},
	}}
}
func (p *shipdProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.APIKey.IsUnknown() || config.APIURL.IsUnknown() || config.OrganizationID.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider configuration", "Provider credentials, URL, and organization guard must be known before planning.")
		return
	}
	token := os.Getenv("SHIPD_API_KEY")
	if !config.APIKey.IsNull() {
		token = config.APIKey.ValueString()
	}
	baseURL := os.Getenv("SHIPD_API_URL")
	if baseURL == "" {
		baseURL = client.DefaultURL
	}
	if !config.APIURL.IsNull() {
		baseURL = config.APIURL.ValueString()
	}
	orgID := os.Getenv("SHIPD_ORGANIZATION_ID")
	if !config.OrganizationID.IsNull() {
		orgID = config.OrganizationID.ValueString()
	}
	c, err := client.New(baseURL, token)
	if err != nil {
		resp.Diagnostics.AddError("Invalid shipd provider configuration", err.Error())
		return
	}
	if orgID != "" {
		var identity client.Identity
		if err := c.RPC(ctx, "me", struct{}{}, &identity, false); err != nil {
			resp.Diagnostics.AddError("Cannot verify organization", err.Error())
			return
		}
		if identity.Organization.ID != orgID {
			resp.Diagnostics.AddError("Organization mismatch", "The credential belongs to a different organization than organization_id. Use credentials for the intended organization.")
			return
		}
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}
func (p *shipdProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &projectResource{} }, func() resource.Resource { return &environmentResource{} }}
}
func (p *shipdProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{func() datasource.DataSource { return &lookupDataSource{} }, func() datasource.DataSource { return &lookupDataSource{environment: true} }}
}
