package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

var kubernetesVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var kubernetesTalosVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

type kubernetesRegionDataSource struct{ client *tatnet.ClientWithResponses }
type kubernetesRegionModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	IsDefault           types.Bool   `tfsdk:"is_default"`
	K8sVersions         types.List   `tfsdk:"k8s_versions"`
	TalosVersions       types.List   `tfsdk:"talos_versions"`
	DefaultK8sVersion   types.String `tfsdk:"default_k8s_version"`
	DefaultTalosVersion types.String `tfsdk:"default_talos_version"`
}

var _ datasource.DataSourceWithConfigure = (*kubernetesRegionDataSource)(nil)

func NewKubernetesRegionDataSource() datasource.DataSource { return &kubernetesRegionDataSource{} }
func (*kubernetesRegionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_region"
}
func (*kubernetesRegionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Read Kubernetes and regional Talos versions offered for new clusters. Does not create a cluster or retrieve credentials.", Attributes: map[string]schema.Attribute{
		"id":   schema.StringAttribute{Required: true, Description: "Region UUID. Requires k8s_cluster:read."},
		"name": schema.StringAttribute{Computed: true}, "is_default": schema.BoolAttribute{Computed: true},
		"k8s_versions": schema.ListAttribute{Computed: true, ElementType: types.StringType}, "talos_versions": schema.ListAttribute{Computed: true, ElementType: types.StringType},
		"default_k8s_version": schema.StringAttribute{Computed: true}, "default_talos_version": schema.StringAttribute{Computed: true},
	}}
}
func (d *kubernetesRegionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	d.client, ok = req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
	}
}
func kubernetesVersionListValid(versions []string, defaultVersion string, pattern *regexp.Regexp) bool {
	seen := map[string]bool{}
	for _, v := range versions {
		if !pattern.MatchString(v) || seen[v] {
			return false
		}
		seen[v] = true
	}
	return seen[defaultVersion]
}
func (d *kubernetesRegionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m kubernetesRegionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.ID.IsUnknown() || !vpcUUID.MatchString(m.ID.ValueString()) {
		resp.Diagnostics.AddError("Invalid Kubernetes region", "id must be a known region UUID.")
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure TatNet before reading Kubernetes regions.")
		return
	}
	res, err := d.client.KubernetesListRegionsWithResponse(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read Kubernetes region", "Region request failed; check connectivity.")
		return
	}
	if res.StatusCode() != 200 {
		resp.Diagnostics.AddError("Cannot read Kubernetes region", fmt.Sprintf("Regions returned HTTP %d; check k8s_cluster:read permission.", res.StatusCode()))
		return
	}
	if res.JSON200 == nil || !kubernetesVersionListValid(res.JSON200.K8sVersions, res.JSON200.DefaultK8sVersion, kubernetesVersion) {
		resp.Diagnostics.AddError("Invalid Kubernetes discovery", "API returned invalid Kubernetes versions.")
		return
	}
	var selected *tatnet.V1K8sRegion
	for i := range res.JSON200.Regions {
		r := &res.JSON200.Regions[i]
		if strings.EqualFold(r.Id, m.ID.ValueString()) {
			if selected != nil {
				resp.Diagnostics.AddError("Invalid Kubernetes discovery", "API returned duplicate region IDs.")
				return
			}
			selected = r
		}
	}
	if selected == nil {
		resp.Diagnostics.AddError("Kubernetes region unavailable", "Requested region is not offered for Kubernetes.")
		return
	}
	if strings.TrimSpace(selected.Name) == "" || selected.TalosVersions == nil || selected.DefaultTalosVersion == nil || !kubernetesVersionListValid(*selected.TalosVersions, *selected.DefaultTalosVersion, kubernetesTalosVersion) {
		resp.Diagnostics.AddError("Invalid Kubernetes discovery", "API returned invalid regional Talos versions.")
		return
	}
	m.Name = types.StringValue(selected.Name)
	m.IsDefault = types.BoolValue(selected.IsDefault)
	m.DefaultK8sVersion = types.StringValue(res.JSON200.DefaultK8sVersion)
	m.DefaultTalosVersion = types.StringPointerValue(selected.DefaultTalosVersion)
	k8sVersions, diags := types.ListValueFrom(ctx, types.StringType, res.JSON200.K8sVersions)
	resp.Diagnostics.Append(diags...)
	talosVersions, diags := types.ListValueFrom(ctx, types.StringType, *selected.TalosVersions)
	resp.Diagnostics.Append(diags...)
	m.K8sVersions, m.TalosVersions = k8sVersions, talosVersions
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}
