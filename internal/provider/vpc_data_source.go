package provider

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

var errVPCNotFound = errors.New("VPC returned HTTP 404; check the ID, account access and vpc:read permission")

var vpcUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type vpcDataSource struct{ client *tatnet.ClientWithResponses }
type vpcModel struct {
	ID        types.String `tfsdk:"id"`
	ClusterID types.String `tfsdk:"cluster_id"`
	Name      types.String `tfsdk:"name"`
	Subnet    types.String `tfsdk:"subnet"`
	Status    types.String `tfsdk:"status"`
	IsDefault types.Bool   `tfsdk:"is_default"`
}

var _ datasource.DataSourceWithConfigure = (*vpcDataSource)(nil)

func NewVPCDataSource() datasource.DataSource { return &vpcDataSource{} }
func (*vpcDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}
func (*vpcDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Read an existing account-accessible VPC. Does not create or modify networking.", Attributes: map[string]schema.Attribute{
		"id":         schema.StringAttribute{Required: true, Description: "Existing VPC UUID. Requires vpc:read on the selected VPC."},
		"cluster_id": schema.StringAttribute{Computed: true, Description: "Region UUID reported by the API. Use a precondition to match the VM region."},
		"name":       schema.StringAttribute{Computed: true, Description: "VPC display name."},
		"subnet":     schema.StringAttribute{Computed: true, Description: "Canonical IPv4 CIDR reported by the API."},
		"status":     schema.StringAttribute{Computed: true, Description: "Observed status, or null if omitted. Reading does not prove VM connectivity."},
		"is_default": schema.BoolAttribute{Computed: true, Description: "Whether this is the regional default VPC; null if omitted by the API."},
	}}
}
func (d *vpcDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *vpcDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m vpcModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.ID.IsNull() || m.ID.IsUnknown() || !vpcUUID.MatchString(m.ID.ValueString()) {
		resp.Diagnostics.AddError("Invalid VPC ID", "id must be a known VPC UUID.")
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the TatNet provider before reading a VPC.")
		return
	}
	v, err := lookupVPC(ctx, d.client, m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read TatNet VPC", err.Error())
		return
	}
	m.ClusterID = types.StringValue(v.ClusterId)
	m.Name = types.StringValue(v.Name)
	m.Subnet = types.StringValue(v.Subnet)
	m.Status = types.StringPointerValue(v.Status)
	m.IsDefault = types.BoolPointerValue(v.IsDefault)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func lookupVPC(ctx context.Context, client *tatnet.ClientWithResponses, id string) (*tatnet.V1Vpc, error) {
	res, err := client.NetworkingGetVpcWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("VPC request failed; check connectivity and API availability")
	}
	if res.StatusCode() == 404 {
		return nil, errVPCNotFound
	}
	if res.StatusCode() != 200 {
		return nil, fmt.Errorf("VPC returned HTTP %d; check the ID, account access and vpc:read permission", res.StatusCode())
	}
	v := res.JSON200
	if v == nil || !strings.EqualFold(v.Id, id) || !vpcUUID.MatchString(v.ClusterId) || strings.TrimSpace(v.Name) == "" {
		return nil, fmt.Errorf("VPC returned an invalid response")
	}
	subnet, err := netip.ParsePrefix(v.Subnet)
	if err != nil || !subnet.Addr().Is4() || subnet.Masked().String() != v.Subnet {
		return nil, fmt.Errorf("VPC returned an invalid IPv4 subnet")
	}
	return v, nil
}
