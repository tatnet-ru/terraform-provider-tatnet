package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

type natGatewayResource struct {
	client                         *tatnet.ClientWithResponses
	pollInterval, operationTimeout time.Duration
}
type natGatewayModel struct {
	ID        types.String `tfsdk:"id"`
	VPCID     types.String `tfsdk:"vpc_id"`
	ClusterID types.String `tfsdk:"cluster_id"`
	Address   types.String `tfsdk:"address"`
	Status    types.String `tfsdk:"status"`
	Enabled   types.Bool   `tfsdk:"enabled"`
}

var _ resource.ResourceWithConfigure = (*natGatewayResource)(nil)
var _ resource.ResourceWithValidateConfig = (*natGatewayResource)(nil)
var _ resource.ResourceWithImportState = (*natGatewayResource)(nil)

func NewNATGatewayResource() resource.Resource {
	return &natGatewayResource{pollInterval: 5 * time.Second, operationTimeout: 10 * time.Minute}
}
func (*natGatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nat_gateway"
}
func (*natGatewayResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "VPC egress NAT with a newly allocated public IPv4 address, billed until released. Disable waits for networking teardown and automatic IP release, up to ten minutes. Do not manage the same address as tatnet_floating_ip.", Attributes: map[string]schema.Attribute{
		"id":         schema.StringAttribute{Computed: true, Description: "Owned floating IP UUID, retained on failures.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"vpc_id":     schema.StringAttribute{Required: true, Description: "VPC UUID. Changes replace the gateway.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"cluster_id": schema.StringAttribute{Computed: true, Description: "VPC region UUID."},
		"address":    schema.StringAttribute{Computed: true, Description: "Public IPv4 used for egress."},
		"status":     schema.StringAttribute{Computed: true, Description: "Observed state; attached does not prove internet reachability."},
		"enabled":    schema.BoolAttribute{Computed: true, Description: "Whether the gateway is enabled in the API."},
	}}
}
func (*natGatewayResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m natGatewayModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.VPCID.IsUnknown() && !vpcUUID.MatchString(m.VPCID.ValueString()) {
		resp.Diagnostics.AddError("Invalid NAT VPC", "vpc_id must be a VPC UUID.")
	}
}
func (r *natGatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
	}
}
func (*natGatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || !vpcUUID.MatchString(parts[0]) || !vpcUUID.MatchString(parts[1]) {
		resp.Diagnostics.AddError("Invalid NAT import ID", "Use VPC_UUID/FLOATING_IP_UUID to identify the exact gateway allocation.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
func (r *natGatewayResource) timeout(ctx context.Context) (context.Context, context.CancelFunc) {
	duration := r.operationTimeout
	if duration <= 0 {
		duration = 10 * time.Minute
	}
	return context.WithTimeout(ctx, duration)
}
func (r *natGatewayResource) pause(ctx context.Context) error {
	interval := r.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("timed out or cancelled; allocation ID retained")
	case <-time.After(interval):
		return nil
	}
}
func (r *natGatewayResource) vpc(ctx context.Context, id string) (*tatnet.V1Vpc, error) {
	if r.client == nil {
		return nil, fmt.Errorf("provider is not configured; state retained")
	}
	if !vpcUUID.MatchString(id) {
		return nil, fmt.Errorf("invalid VPC UUID; state retained")
	}
	return lookupVPC(ctx, r.client, id)
}
func checkNAT(g *tatnet.V1NatGateway, id string) error {
	if g == nil || !vpcUUID.MatchString(g.FipId) || (id != "" && !strings.EqualFold(g.FipId, id)) {
		return fmt.Errorf("gateway allocation is missing or differs from the owned IP; state retained")
	}
	address, err := netip.ParseAddr(g.Address)
	if err != nil || !address.Is4() {
		return fmt.Errorf("gateway returned an invalid IPv4 address; state retained")
	}
	switch g.Status {
	case "attaching", "attached", "detaching", "error":
		return nil
	default:
		return fmt.Errorf("gateway returned an unknown status; state retained")
	}
}
func (m *natGatewayModel) observe(g *tatnet.V1NatGateway) {
	m.ID = types.StringValue(g.FipId)
	m.Address = types.StringValue(g.Address)
	m.Status = types.StringValue(g.Status)
	m.Enabled = types.BoolValue(g.Enabled)
}

// Only read the exact allocation. Never release a manually retained or retargeted IP.
func (r *natGatewayResource) allocation(ctx context.Context, m natGatewayModel, allowAttached bool) (bool, error) {
	if !vpcUUID.MatchString(m.ID.ValueString()) {
		return false, fmt.Errorf("invalid allocation UUID; state retained")
	}
	res, err := r.client.NetworkingGetFloatingIpWithResponse(ctx, m.ID.ValueString())
	if err != nil {
		return false, fmt.Errorf("IP read failed; state retained")
	}
	if res.StatusCode() == 404 {
		return false, nil
	}
	if res.StatusCode() != 200 {
		return false, fmt.Errorf("IP read returned HTTP %d; state retained", res.StatusCode())
	}
	f := res.JSON200
	if f == nil || !strings.EqualFold(f.Id, m.ID.ValueString()) || !vpcUUID.MatchString(f.ClusterId) || (!m.ClusterID.IsNull() && !m.ClusterID.IsUnknown() && !strings.EqualFold(f.ClusterId, m.ClusterID.ValueString())) {
		return false, fmt.Errorf("IP returned an incomplete or mismatched allocation; state retained")
	}
	if f.VmInterfaceId != nil || f.TargetKind == nil {
		return false, fmt.Errorf("IP ownership changed or cannot be confirmed; state retained")
	}
	attached := allowAttached && *f.TargetKind == "vpc_nat" && f.VpcId != nil && strings.EqualFold(*f.VpcId, m.VPCID.ValueString())
	releasing := *f.TargetKind == "interface" && f.VpcId == nil && f.AutoRelease != nil && *f.AutoRelease
	if !attached && !releasing {
		return false, fmt.Errorf("IP is manually retained or retargeted; reconcile ownership before retrying; state retained")
	}
	return true, nil
}
func (r *natGatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m natGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !vpcUUID.MatchString(m.VPCID.ValueString()) {
		resp.Diagnostics.AddError("Invalid NAT VPC", "vpc_id must be known before creation.")
		return
	}
	ctx, cancel := r.timeout(ctx)
	defer cancel()
	v, err := r.vpc(ctx, m.VPCID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot create NAT gateway", err.Error())
		return
	}
	if v.Status == nil || *v.Status != "active" || v.NatGateway != nil {
		resp.Diagnostics.AddError("Cannot create NAT gateway", "The VPC must be active and have no existing or detaching gateway. Import an existing gateway explicitly.")
		return
	}
	m.ClusterID = types.StringValue(v.ClusterId)
	res, err := r.client.NetworkingEnableNatGatewayWithResponse(ctx, m.VPCID.ValueString(), tatnet.V1NatGatewayEnable{})
	if err != nil {
		resp.Diagnostics.AddError("NAT allocation outcome unknown", "Inspect the VPC and account before retrying; import any allocated gateway using VPC_UUID/FLOATING_IP_UUID. No automatic POST retry was made.")
		return
	}
	if res.StatusCode() != 201 || res.JSON201 == nil || !vpcUUID.MatchString(res.JSON201.FipId) {
		resp.Diagnostics.AddError("Cannot create NAT gateway", fmt.Sprintf("Enable returned HTTP %d without a usable allocation ID. Inspect the VPC and account before retrying.", res.StatusCode()))
		return
	}
	// Record identity even if the response or subsequent provisioning is invalid.
	m.observe(res.JSON201)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err = checkNAT(res.JSON201, m.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Cannot reconcile NAT gateway", err.Error())
		return
	}
	for {
		v, err = r.vpc(ctx, m.VPCID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Cannot reconcile NAT gateway", err.Error()+"; allocation ID retained")
			return
		}
		if !strings.EqualFold(v.ClusterId, m.ClusterID.ValueString()) {
			resp.Diagnostics.AddError("Cannot reconcile NAT gateway", "VPC region changed; allocation ID retained.")
			return
		}
		if err = checkNAT(v.NatGateway, m.ID.ValueString()); err != nil {
			resp.Diagnostics.AddError("Cannot reconcile NAT gateway", err.Error())
			return
		}
		m.observe(v.NatGateway)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if m.Status.ValueString() == "error" || !m.Enabled.ValueBool() {
			resp.Diagnostics.AddError("NAT provisioning failed", "Gateway failed or was disabled during provisioning; allocation ID retained.")
			return
		}
		if m.Status.ValueString() == "attached" {
			return
		}
		if err = r.pause(ctx); err != nil {
			resp.Diagnostics.AddError("NAT provisioning timeout", err.Error())
			return
		}
	}
}
func (r *natGatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m natGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !vpcUUID.MatchString(m.ID.ValueString()) {
		resp.Diagnostics.AddError("Cannot read NAT gateway", "Invalid allocation UUID; state retained.")
		return
	}
	v, err := r.vpc(ctx, m.VPCID.ValueString())
	if err != nil && !errors.Is(err, errVPCNotFound) {
		resp.Diagnostics.AddError("Cannot read NAT gateway", err.Error())
		return
	}
	if v != nil && v.NatGateway != nil {
		if err = checkNAT(v.NatGateway, m.ID.ValueString()); err != nil {
			resp.Diagnostics.AddError("Cannot read NAT gateway", err.Error())
			return
		}
		m.ClusterID = types.StringValue(v.ClusterId)
		m.observe(v.NatGateway)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	exists, err := r.allocation(ctx, m, v != nil && v.NatGateway != nil)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read NAT gateway", err.Error())
		return
	}
	if !exists {
		resp.State.RemoveResource(ctx)
		return
	}
	if v == nil {
		resp.Diagnostics.AddError("Cannot read NAT gateway", "VPC is missing while its allocated IP remains; reconcile ownership; state retained.")
		return
	}
	m.Enabled = types.BoolValue(false)
	m.Status = types.StringValue("detaching")
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (*natGatewayResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("NAT update unsupported", "Changing vpc_id requires replacement.")
}
func (r *natGatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m natGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !vpcUUID.MatchString(m.ID.ValueString()) {
		resp.Diagnostics.AddError("Cannot disable NAT gateway", "Invalid allocation UUID; state retained.")
		return
	}
	ctx, cancel := r.timeout(ctx)
	defer cancel()
	v, err := r.vpc(ctx, m.VPCID.ValueString())
	if err != nil && !errors.Is(err, errVPCNotFound) {
		resp.Diagnostics.AddError("Cannot disable NAT gateway", err.Error())
		return
	}
	if v != nil && v.NatGateway != nil {
		if err = checkNAT(v.NatGateway, m.ID.ValueString()); err != nil {
			resp.Diagnostics.AddError("Cannot disable NAT gateway", err.Error())
			return
		}
		if v.NatGateway.Enabled {
			res, e := r.client.NetworkingDisableNatGatewayWithResponse(ctx, m.VPCID.ValueString(), func(_ context.Context, req *http.Request) error {
				query := req.URL.Query()
				query.Set("expected_fip_id", m.ID.ValueString())
				req.URL.RawQuery = query.Encode()
				return nil
			})
			if e != nil {
				resp.Diagnostics.AddError("NAT disable outcome unknown", "Refresh before retrying; allocation ID retained.")
				return
			}
			if res.StatusCode() != 202 && res.StatusCode() != 404 {
				resp.Diagnostics.AddError("Cannot disable NAT gateway", fmt.Sprintf("Disable returned HTTP %d; state retained.", res.StatusCode()))
				return
			}
		}
	}
	for {
		v, err = r.vpc(ctx, m.VPCID.ValueString())
		if err != nil && !errors.Is(err, errVPCNotFound) {
			resp.Diagnostics.AddError("Cannot wait for NAT release", err.Error())
			return
		}
		if v != nil && v.NatGateway != nil {
			if err = checkNAT(v.NatGateway, m.ID.ValueString()); err != nil {
				resp.Diagnostics.AddError("Cannot wait for NAT release", err.Error())
				return
			}
			if v.NatGateway.Status == "error" {
				resp.Diagnostics.AddError("NAT release failed", "Networking reported an error; state retained.")
				return
			}
		}
		exists, e := r.allocation(ctx, m, v != nil && v.NatGateway != nil)
		if e != nil {
			resp.Diagnostics.AddError("Cannot wait for NAT release", e.Error())
			return
		}
		if !exists {
			if v != nil && v.NatGateway != nil {
				resp.Diagnostics.AddError("Cannot confirm NAT release", "The VPC still reports a gateway for a missing allocation; state retained.")
				return
			}
			resp.State.RemoveResource(ctx)
			return
		}
		if err = r.pause(ctx); err != nil {
			resp.Diagnostics.AddError("NAT release timeout", err.Error())
			return
		}
	}
}
