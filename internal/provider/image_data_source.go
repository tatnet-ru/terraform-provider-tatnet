package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

type imageDataSource struct{ client *tatnet.ClientWithResponses }
type imageModel struct {
	ProjectID types.String `tfsdk:"project_id"`
	ClusterID types.String `tfsdk:"cluster_id"`
	Family    types.String `tfsdk:"family"`
	Version   types.String `tfsdk:"version"`
	ID        types.String `tfsdk:"id"`
}

var _ datasource.DataSourceWithConfigure = (*imageDataSource)(nil)

func NewImageDataSource() datasource.DataSource { return &imageDataSource{} }
func (*imageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}
func (*imageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Select an account-accessible OS image deployed in the requested region.", Attributes: map[string]schema.Attribute{
		"project_id": schema.StringAttribute{Required: true, Description: "Project UUID. Account and permissions come from the API key."},
		"cluster_id": schema.StringAttribute{Required: true, Description: "Target region (cluster UUID)."},
		"family":     schema.StringAttribute{Required: true, Description: "OS family slug, for example debian or redos."},
		"version":    schema.StringAttribute{Required: true, Description: "Family version, for example 13 or 7.3. Resolves the currently available regional build."},
		"id":         schema.StringAttribute{Computed: true, Description: "Image UUID available in the requested region. May change when a new build is published and permitted."},
	}}
}
func (d *imageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
		return
	}
	d.client = client
}
func (d *imageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data imageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, v := range []types.String{data.ProjectID, data.ClusterID, data.Family, data.Version} {
		if v.IsNull() || v.IsUnknown() || strings.TrimSpace(v.ValueString()) == "" {
			resp.Diagnostics.AddError("Missing image selector", "project_id, cluster_id, family and version must be known and non-empty.")
			return
		}
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the TatNet provider before reading images.")
		return
	}
	id, err := lookupImage(ctx, d.client, data.ProjectID.ValueString(), data.ClusterID.ValueString(), data.Family.ValueString(), data.Version.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read TatNet image", err.Error())
		return
	}
	data.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func lookupImage(ctx context.Context, client *tatnet.ClientWithResponses, project, cluster, family, version string) (string, error) {
	response, err := client.VmsListImagesWithResponse(ctx, project, &tatnet.VmsListImagesParams{ClusterId: &cluster})
	if err != nil {
		return "", fmt.Errorf("image catalogue request failed; check connectivity and API availability")
	}
	if response.StatusCode() != 200 {
		return "", fmt.Errorf("image catalogue returned HTTP %d; check the API key, project access and vm:read permission", response.StatusCode())
	}
	if response.JSON200 == nil {
		return "", fmt.Errorf("image catalogue returned an invalid response")
	}
	catalogue := response.JSON200
	if catalogue.Families != nil {
		for _, f := range *catalogue.Families {
			if f.Slug != family || f.Kind != "os" || f.Versions == nil {
				continue
			}
			for _, v := range *f.Versions {
				if v.Version != version || v.ByCluster == nil {
					continue
				}
				id := (*v.ByCluster)[cluster]
				if id == "" {
					continue
				}
				for _, image := range catalogue.Images {
					if image.Id == id {
						return id, nil
					}
				}
			}
		}
	}
	return "", fmt.Errorf("no accessible OS image matches family %q, version %q in region %q; check image permissions and regional availability", family, version, cluster)
}
