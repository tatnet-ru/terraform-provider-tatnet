package provider

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

type vpcResource struct {
	client                         *tatnet.ClientWithResponses
	pollInterval, operationTimeout time.Duration
}

var _ resource.ResourceWithConfigure = (*vpcResource)(nil)
var _ resource.ResourceWithValidateConfig = (*vpcResource)(nil)
var _ resource.ResourceWithImportState = (*vpcResource)(nil)

func NewVPCResource() resource.Resource {
	return &vpcResource{pollInterval: 5 * time.Second, operationTimeout: 10 * time.Minute}
}
func (*vpcResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}
func (*vpcResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "Manage an account VPC. Creation waits up to ten minutes for active status. Input changes replace the network. Default VPCs cannot be deleted. The API must enforce occupied-network deletion guards.", Attributes: map[string]schema.Attribute{
		"id":         schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"cluster_id": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Region UUID. Changes replace the VPC."},
		"name":       schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Display name (1–255 characters). Changes replace the VPC."},
		"subnet":     schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Canonical IPv4 CIDR; the API validates reserved ranges and overlaps. Changes replace the VPC."},
		"status":     schema.StringAttribute{Computed: true},
		"is_default": schema.BoolAttribute{Computed: true},
	}}
}
func validateVPCConfig(m vpcModel) error {
	if !m.ClusterID.IsUnknown() && !vpcUUID.MatchString(m.ClusterID.ValueString()) {
		return fmt.Errorf("cluster_id must be a region UUID")
	}
	if !m.Name.IsUnknown() && (strings.TrimSpace(m.Name.ValueString()) == "" || utf8.RuneCountInString(m.Name.ValueString()) > 255) {
		return fmt.Errorf("name must contain 1–255 characters")
	}
	if !m.Subnet.IsUnknown() {
		p, err := netip.ParsePrefix(m.Subnet.ValueString())
		if err != nil || !p.Addr().Is4() || p.Masked().String() != m.Subnet.ValueString() {
			return fmt.Errorf("subnet must be a canonical IPv4 CIDR without host bits")
		}
	}
	return nil
}
func (*vpcResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m vpcModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateVPCConfig(m); err != nil {
		resp.Diagnostics.AddError("Invalid VPC configuration", err.Error())
	}
}
func (r *vpcResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
	}
}
func (*vpcResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !vpcUUID.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid VPC ID", "Import using the VPC UUID.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
func (m *vpcModel) observeVPC(v *tatnet.V1Vpc) {
	m.ID = types.StringValue(v.Id)
	m.ClusterID = types.StringValue(v.ClusterId)
	m.Name = types.StringValue(v.Name)
	m.Subnet = types.StringValue(v.Subnet)
	m.Status = types.StringPointerValue(v.Status)
	m.IsDefault = types.BoolPointerValue(v.IsDefault)
}
func (r *vpcResource) get(ctx context.Context, id string) (*tatnet.V1Vpc, error) {
	if r.client == nil {
		return nil, fmt.Errorf("provider is not configured; state retained")
	}
	if !vpcUUID.MatchString(id) {
		return nil, fmt.Errorf("invalid VPC ID; state retained")
	}
	return lookupVPC(ctx, r.client, id)
}
func (r *vpcResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m vpcModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateVPCConfig(m); err != nil {
		resp.Diagnostics.AddError("Invalid VPC configuration", err.Error())
		return
	}
	if m.ClusterID.IsUnknown() || m.Name.IsUnknown() || m.Subnet.IsUnknown() {
		resp.Diagnostics.AddError("Invalid VPC configuration", "VPC inputs must be known before creation.")
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the TatNet provider before creating a VPC.")
		return
	}
	timeout := r.operationTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := r.client.NetworkingCreateVpcWithResponse(ctx, tatnet.V1VpcCreate{ClusterId: m.ClusterID.ValueString(), Name: m.Name.ValueString(), Subnet: m.Subnet.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError("Cannot create TatNet VPC", "Creation outcome unknown; inspect account VPCs and import any created network before retrying. No automatic retry was made.")
		return
	}
	if res.StatusCode() != 201 || res.JSON201 == nil || !vpcUUID.MatchString(res.JSON201.Id) {
		resp.Diagnostics.AddError("Cannot create TatNet VPC", fmt.Sprintf("Creation returned HTTP %d without a usable creation result; inspect account VPCs before retrying.", res.StatusCode()))
		return
	}
	// Persist identity before polling: a failed wait must not orphan the network.
	m.ID = types.StringValue(res.JSON201.Id)
	m.Status = types.StringPointerValue(res.JSON201.Status)
	m.IsDefault = types.BoolPointerValue(res.JSON201.IsDefault)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	interval := r.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		v, e := r.get(ctx, m.ID.ValueString())
		if e != nil {
			resp.Diagnostics.AddError("Cannot reconcile TatNet VPC", e.Error()+"; created ID retained")
			return
		}
		if !strings.EqualFold(v.ClusterId, m.ClusterID.ValueString()) || v.Name != m.Name.ValueString() || v.Subnet != m.Subnet.ValueString() {
			resp.Diagnostics.AddError("VPC configuration mismatch", "API returned different VPC inputs; created ID retained for reconciliation.")
			return
		}
		plannedRegion := m.ClusterID
		m.observeVPC(v)
		m.ClusterID = plannedRegion // Preserve equivalent UUID spelling from the plan.
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if v.IsDefault == nil || *v.IsDefault {
			resp.Diagnostics.AddError("Cannot manage default VPC", "API did not confirm a non-default network; created ID retained.")
			return
		}
		if v.Status != nil && *v.Status == "active" {
			return
		}
		if v.Status != nil && (*v.Status == "error" || *v.Status == "deleting") {
			resp.Diagnostics.AddError("VPC provisioning failed", "Created ID retained; inspect the network before retrying.")
			return
		}
		select {
		case <-ctx.Done():
			resp.Diagnostics.AddError("VPC provisioning timeout", "Timed out or cancelled waiting for active status; created ID retained.")
			return
		case <-time.After(interval):
		}
	}
}
func (r *vpcResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m vpcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.get(ctx, m.ID.ValueString())
	if errors.Is(err, errVPCNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read TatNet VPC", err.Error()+"; state retained")
		return
	}
	plannedRegion := m.ClusterID
	m.observeVPC(v)
	if strings.EqualFold(plannedRegion.ValueString(), v.ClusterId) {
		m.ClusterID = plannedRegion
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (*vpcResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("VPC update unsupported", "VPC input changes must replace the resource.")
}
func (r *vpcResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m vpcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.get(ctx, m.ID.ValueString())
	if errors.Is(err, errVPCNotFound) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot delete TatNet VPC", err.Error()+"; state retained")
		return
	}
	if v.IsDefault == nil || *v.IsDefault {
		resp.Diagnostics.AddError("Cannot delete default VPC", "API must confirm a non-default VPC before deletion; state retained. Use the data source for default networks.")
		return
	}
	if v.NatGateway != nil {
		resp.Diagnostics.AddError("Cannot delete VPC with NAT", "Remove the NAT gateway before deletion; state retained.")
		return
	}
	res, err := r.client.NetworkingDeleteVpcWithResponse(ctx, m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot delete TatNet VPC", "Deletion outcome unknown; refresh before retrying; state retained.")
		return
	}
	if res.StatusCode() != 204 && res.StatusCode() != 404 {
		resp.Diagnostics.AddError("Cannot delete TatNet VPC", fmt.Sprintf("Delete returned HTTP %d; detach dependent resources before retrying; state retained.", res.StatusCode()))
	}
	// 204 confirms control-plane removal; OVN teardown remains asynchronous.
}
