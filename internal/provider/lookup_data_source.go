package provider

import (
	"context"
	"github.com/alasti-company/terraform-provider-shipd/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type lookupDataSource struct {
	client      *client.Client
	environment bool
}

func (d *lookupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
	if d.environment {
		resp.TypeName = req.ProviderTypeName + "_environment"
	}
}
func (d *lookupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id":      schema.StringAttribute{Optional: true, Computed: true, Description: "Object ID. Specify exactly one of id or slug.", Validators: []validator.String{stringvalidator.LengthAtLeast(1), stringvalidator.ExactlyOneOf(path.MatchRoot("slug"))}},
		"slug":    schema.StringAttribute{Optional: true, Computed: true, Description: "Object slug. Specify exactly one of slug or id.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
		"name":    schema.StringAttribute{Computed: true, Description: "Display name."},
		"version": schema.Int64Attribute{Computed: true, Description: "Current API object version."},
	}
	if d.environment {
		attrs["project_id"] = schema.StringAttribute{Required: true, Description: "Project containing the environment.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}}
		attrs["type"] = schema.StringAttribute{Computed: true, Description: "Free-form environment classification."}
		attrs["is_production"] = schema.BoolAttribute{Computed: true, Description: "Whether the environment is production."}
	} else {
		attrs["organization_id"] = schema.StringAttribute{Computed: true, Description: "Organization containing the project."}
	}
	resp.Schema = schema.Schema{Description: "Look up an existing shipd object by ID or slug without managing its lifecycle.", Attributes: attrs}
}
func (d *lookupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	configureClient(req.ProviderData, &d.client, &resp.Diagnostics)
}
func (d *lookupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// Use Config rather than previous state so a computed ID from an earlier read
	// never takes precedence over a configured slug after a refresh.
	var id, slug types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("slug"), &slug)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.environment {
		var projectID types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_id"), &projectID)...)
		if resp.Diagnostics.HasError() {
			return
		}
		e, err := d.client.Environment(ctx, projectID.ValueString(), id.ValueString(), slug.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Cannot look up environment", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, environmentState(e))...)
	} else {
		p, err := d.client.Project(ctx, id.ValueString(), slug.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Cannot look up project", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, projectState(p))...)
	}
}
