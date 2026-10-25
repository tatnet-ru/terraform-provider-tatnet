package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

type vmResource struct {
	client                         *tatnet.ClientWithResponses
	pollInterval, operationTimeout time.Duration
}
type vmModel struct {
	ID                 types.String `tfsdk:"id"`
	InterfaceID        types.String `tfsdk:"primary_interface_id"`
	ProjectID          types.String `tfsdk:"project_id"`
	ClusterID          types.String `tfsdk:"cluster_id"`
	ImageID            types.String `tfsdk:"image_id"`
	PlanID             types.String `tfsdk:"vm_plan_id"`
	Name               types.String `tfsdk:"name"`
	Hostname           types.String `tfsdk:"hostname"`
	DefaultUser        types.String `tfsdk:"default_user"`
	VPCID              types.String `tfsdk:"vpc_id"`
	SSHKeys            types.Set    `tfsdk:"ssh_key_ids"`
	PeriodDays         types.Int64  `tfsdk:"period_days"`
	PeriodMonths       types.Int64  `tfsdk:"period_months"`
	BillingPeriod      types.String `tfsdk:"billing_period"`
	SubscriptionPeriod types.String `tfsdk:"subscription_period"`
	AmountPerPeriod    types.String `tfsdk:"amount_per_period"`
	Currency           types.String `tfsdk:"currency"`
	AutoRenew          types.Bool   `tfsdk:"auto_renew"`
	Status             types.String `tfsdk:"status"`
	IPv4               types.List   `tfsdk:"ipv4_addresses"`
}

var _ resource.ResourceWithConfigure = (*vmResource)(nil)
var _ resource.ResourceWithValidateConfig = (*vmResource)(nil)

func NewVMResource() resource.Resource {
	return &vmResource{pollInterval: 5 * time.Second, operationTimeout: 20 * time.Minute}
}
func (*vmResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}
func (*vmResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	required := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Required: true, Description: description, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}}
	}
	resp.Schema = schema.Schema{Description: "VM with one private interface in an existing VPC. Subscription period and automatic renewal update in place; changes to VM creation parameters replace the VM. Create/delete wait up to 20 minutes.", Attributes: map[string]schema.Attribute{
		"id":         schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"project_id": required("Project UUID."), "cluster_id": required("Region UUID."), "image_id": required("Accessible image UUID deployed in the region."), "vm_plan_id": required("VM tariff/plan UUID; CPU, memory and disk use plan defaults."),
		"name": required("VM display name."), "hostname": required("VM hostname."), "default_user": required("SSH login user."), "vpc_id": required("Existing VPC UUID in the target region. No public IP is allocated."),
		"ssh_key_ids":          schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "At least one existing SSH key UUID.", PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}},
		"period_days":          schema.Int64Attribute{Optional: true, Description: "Daily subscription: only 1 is supported. Use period_months=1 for a calendar month. Mutually exclusive with period_months and billing_period."},
		"period_months":        schema.Int64Attribute{Optional: true, Description: "Calendar subscription period: 1, 3, 6, or 12 months. Changes update billing without restarting or replacing the VM."},
		"billing_period":       schema.StringAttribute{Optional: true, Description: "Subscription interval: 1h, 1d, 1m, 3m, 6m, or 12m. Specify exactly one of billing_period, period_days, period_months."},
		"subscription_period":  schema.StringAttribute{Computed: true, Description: "Actual subscription interval reported by billing."},
		"amount_per_period":    schema.StringAttribute{Computed: true, Description: "Server-priced total per subscription period, as a decimal string."},
		"currency":             schema.StringAttribute{Computed: true, Description: "Subscription currency."},
		"auto_renew":           schema.BoolAttribute{Required: true, Description: "Automatically renew the subscription. Changes neither charge nor start a stopped VM."},
		"status":               schema.StringAttribute{Computed: true, Description: "Observed VM lifecycle status; stopped VMs are not started automatically."},
		"primary_interface_id": schema.StringAttribute{Computed: true, Description: "UUID of the sole interface in the configured VPC, suitable for tatnet_floating_ip.vm_interface_id. Null when missing or ambiguous."},
		"ipv4_addresses":       schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Observed IPv4 addresses."},
	}}
}
func (*vmResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m vmModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, v := range map[string]types.String{"project_id": m.ProjectID, "cluster_id": m.ClusterID, "image_id": m.ImageID, "vm_plan_id": m.PlanID, "name": m.Name, "hostname": m.Hostname, "default_user": m.DefaultUser, "vpc_id": m.VPCID} {
		if !v.IsNull() && !v.IsUnknown() && strings.TrimSpace(v.ValueString()) == "" {
			resp.Diagnostics.AddError("Invalid VM configuration", name+" must not be empty.")
		}
	}
	if !m.PeriodDays.IsUnknown() && !m.PeriodMonths.IsUnknown() && !m.BillingPeriod.IsUnknown() {
		if _, err := m.paymentPeriod(); err != nil {
			resp.Diagnostics.AddError("Invalid payment period", err.Error())
		}
	}
	if !m.SSHKeys.IsNull() && !m.SSHKeys.IsUnknown() {
		if len(m.SSHKeys.Elements()) == 0 {
			resp.Diagnostics.AddError("Missing SSH key", "Provide at least one SSH key ID.")
		}
		for _, v := range m.SSHKeys.Elements() {
			if !v.IsUnknown() && !v.IsNull() && strings.TrimSpace(v.(types.String).ValueString()) == "" || v.IsNull() {
				resp.Diagnostics.AddError("Invalid SSH key", "SSH key IDs must not be empty or null.")
			}
		}
	}
}
func (r *vmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
	}
}
func (r *vmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure TatNet before creating a VM.")
		return
	}
	var keys []string
	resp.Diagnostics.Append(m.SSHKeys.ElementsAs(ctx, &keys, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	period, err := m.paymentPeriod()
	if err != nil {
		resp.Diagnostics.AddError("Invalid payment period", err.Error())
		return
	}
	var days, months *int
	if period == "1d" {
		v := 1
		days = &v
	} else if strings.HasSuffix(period, "m") {
		v, _ := strconv.Atoi(strings.TrimSuffix(period, "m"))
		months = &v
	}
	renew := m.AutoRenew.ValueBool()
	vpc := m.VPCID.ValueString()
	no := false
	yes := true
	interfaces := []tatnet.V1VMInterface{{Type: "vpc", VpcId: &vpc, FloatingIp: &no, Dhcp4: &yes}}
	result, err := r.client.VmsCreateVmWithResponse(ctx, m.ProjectID.ValueString(), tatnet.V1VMCreate{
		Name: m.Name.ValueString(), Hostname: m.Hostname.ValueString(), ClusterId: m.ClusterID.ValueString(), ImageId: m.ImageID.ValueString(), VmPlanId: m.PlanID.ValueString(), DefaultUser: m.DefaultUser.ValueString(), SshKeyIds: &keys, Interfaces: &interfaces, PeriodDays: days, PeriodMonths: months, AutoRenew: &renew,
	})
	// POST has no idempotency contract. Never retry an ambiguous create automatically.
	if err != nil {
		resp.Diagnostics.AddError("VM creation outcome unknown", "The create request failed. Check the project for a created VM before retrying; this API has no idempotent create token.")
		return
	}
	if result.StatusCode() != 201 || result.JSON201 == nil || result.JSON201.Id == "" {
		resp.Diagnostics.AddError("Cannot create VM", fmt.Sprintf("API returned HTTP %d without a usable VM. Check project permissions, image access, plan and balance before retrying.", result.StatusCode()))
		return
	}
	m.ID = types.StringValue(result.JSON201.Id)
	m.observe(ctx, result.JSON201, &resp.Diagnostics, false)
	// Preserve the ID even when waiting fails: Terraform can destroy the partial VM.
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vm, err := r.wait(ctx, &m, false, result.JSON201)
	if vm != nil {
		m.observe(ctx, vm, &resp.Diagnostics, false)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("VM did not become ready", err.Error())
		return
	}
	if err := r.readSubscription(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Cannot read VM subscription", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m vmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vm, missing, err := r.get(ctx, &m)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read VM", err.Error())
		return
	}
	if missing {
		resp.State.RemoveResource(ctx)
		return
	}
	m.observe(ctx, vm, &resp.Diagnostics, true)
	if err := r.readSubscription(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Cannot read VM subscription", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var desired, prior vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &desired)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !desired.ProjectID.Equal(prior.ProjectID) || !desired.ClusterID.Equal(prior.ClusterID) ||
		!desired.ImageID.Equal(prior.ImageID) || !desired.PlanID.Equal(prior.PlanID) ||
		!desired.Name.Equal(prior.Name) || !desired.Hostname.Equal(prior.Hostname) ||
		!desired.DefaultUser.Equal(prior.DefaultUser) || !desired.VPCID.Equal(prior.VPCID) || !desired.SSHKeys.Equal(prior.SSHKeys) {
		resp.Diagnostics.AddError("VM replacement required", "Only subscription settings can change in place.")
		return
	}
	period, err := desired.paymentPeriod()
	if err != nil {
		resp.Diagnostics.AddError("Invalid payment period", err.Error())
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure TatNet before updating a subscription.")
		return
	}
	desired.ID = prior.ID
	p := tatnet.V1VMSubscriptionUpdatePeriodType(period)
	renew := desired.AutoRenew.ValueBool()
	result, err := r.client.VmsPatchVmSubscriptionWithResponse(ctx, desired.ProjectID.ValueString(), desired.ID.ValueString(), tatnet.V1VMSubscriptionUpdate{PeriodType: &p, AutoRenew: &renew})
	if err != nil {
		resp.Diagnostics.AddError("VM subscription outcome unknown", "Refresh the subscription before retrying; the VM identity is retained.")
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError("Cannot update VM subscription", fmt.Sprintf("API returned HTTP %d. Check billing permission, balance and paid period. The VM was not replaced.", result.StatusCode()))
		return
	}
	if result.JSON200.ResourceId != prior.ID.ValueString() || result.JSON200.PeriodType != period || result.JSON200.AutoRenew != renew {
		resp.Diagnostics.AddError("VM subscription not confirmed", "API returned different subscription settings; refresh before retrying.")
		return
	}
	desired.observeSubscription(result.JSON200)
	// Subscription updates do not change the VM's observed power/network state.
	desired.Status, desired.InterfaceID, desired.IPv4 = prior.Status, prior.InterfaceID, prior.IPv4
	resp.Diagnostics.Append(resp.State.Set(ctx, &desired)...)
}

func (m *vmModel) paymentPeriod() (string, error) {
	count := 0
	period := ""
	if !m.PeriodDays.IsNull() {
		count++
		if m.PeriodDays.IsUnknown() || m.PeriodDays.ValueInt64() != 1 {
			return "", fmt.Errorf("period_days supports only 1; replace period_days=30 with period_months=1")
		}
		period = "1d"
	}
	if !m.PeriodMonths.IsNull() {
		count++
		if m.PeriodMonths.IsUnknown() {
			return "", fmt.Errorf("period_months must be known")
		}
		period = fmt.Sprintf("%dm", m.PeriodMonths.ValueInt64())
	}
	if !m.BillingPeriod.IsNull() {
		count++
		if m.BillingPeriod.IsUnknown() {
			return "", fmt.Errorf("billing_period must be known")
		}
		period = m.BillingPeriod.ValueString()
	}
	if count != 1 {
		return "", fmt.Errorf("specify exactly one of billing_period, period_months, period_days")
	}
	switch period {
	case "1h", "1d", "1m", "3m", "6m", "12m":
		return period, nil
	}
	return "", fmt.Errorf("supported periods: 1h, 1d, 1m, 3m, 6m, 12m")
}

func (r *vmResource) readSubscription(ctx context.Context, m *vmModel) error {
	result, err := r.client.VmsGetVmSubscriptionWithResponse(ctx, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		return fmt.Errorf("subscription read failed; VM state retained")
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		return fmt.Errorf("subscription read returned HTTP %d; VM state retained", result.StatusCode())
	}
	if result.JSON200.ResourceId != m.ID.ValueString() {
		return fmt.Errorf("subscription belongs to another VM; state retained")
	}
	switch result.JSON200.PeriodType {
	case "1h", "1d", "1m", "3m", "6m", "12m":
	default:
		return fmt.Errorf("unsupported subscription interval; state retained")
	}
	m.observeSubscription(result.JSON200)
	return nil
}

func (m *vmModel) observeSubscription(s *tatnet.V1VMSubscription) {
	m.SubscriptionPeriod = types.StringValue(s.PeriodType)
	m.AmountPerPeriod, m.Currency = types.StringValue(s.AmountPerPeriod), types.StringValue(s.Currency)
	m.AutoRenew = types.BoolValue(s.AutoRenew)
	if !m.BillingPeriod.IsNull() {
		m.BillingPeriod = types.StringValue(s.PeriodType)
	}
	if !m.PeriodDays.IsNull() {
		m.PeriodDays = types.Int64Null()
		if s.PeriodType == "1d" {
			m.PeriodDays = types.Int64Value(1)
		}
	}
	if !m.PeriodMonths.IsNull() {
		m.PeriodMonths = types.Int64Null()
		if strings.HasSuffix(s.PeriodType, "m") {
			v, _ := strconv.Atoi(strings.TrimSuffix(s.PeriodType, "m"))
			m.PeriodMonths = types.Int64Value(int64(v))
		}
	}
}
func (r *vmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m vmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure TatNet before deleting a VM.")
		return
	}
	result, err := r.client.VmsDeleteVmWithResponse(ctx, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot delete VM", "Delete request failed; the VM remains tracked. Retry after checking connectivity.")
		return
	}
	if result.StatusCode() == 404 {
		resp.State.RemoveResource(ctx)
		return
	}
	if result.StatusCode() != 202 {
		resp.Diagnostics.AddError("Cannot delete VM", fmt.Sprintf("API returned HTTP %d; the VM remains tracked.", result.StatusCode()))
		return
	}
	if _, err = r.wait(ctx, &m, true, nil); err != nil {
		resp.Diagnostics.AddError("VM deletion not complete", err.Error())
		return
	}
	resp.State.RemoveResource(ctx)
}
func (m *vmModel) observe(ctx context.Context, vm *tatnet.V1VM, diags *diag.Diagnostics, refresh bool) {
	m.Status = types.StringValue(vm.Status)
	m.InterfaceID = types.StringNull()
	if vm.Interfaces != nil {
		matches := []string{}
		for _, iface := range *vm.Interfaces {
			if iface.VpcId != nil && *iface.VpcId == m.VPCID.ValueString() && iface.Id != "" {
				matches = append(matches, iface.Id)
			}
		}
		if len(matches) == 1 {
			m.InterfaceID = types.StringValue(matches[0])
		}
	}
	ips := []string{}
	if vm.Ipv4Addresses != nil {
		ips = *vm.Ipv4Addresses
	}
	value, d := types.ListValueFrom(ctx, types.StringType, ips)
	diags.Append(d...)
	m.IPv4 = value
	if refresh {
		m.Name = types.StringValue(vm.Name)
		m.Hostname = types.StringValue(vm.Hostname)
	}
}
func (r *vmResource) get(ctx context.Context, m *vmModel) (*tatnet.V1VM, bool, error) {
	if r.client == nil {
		return nil, false, fmt.Errorf("provider is not configured")
	}
	result, err := r.client.VmsGetVmWithResponse(ctx, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		return nil, false, fmt.Errorf("VM read request failed; state retained")
	}
	if result.StatusCode() == 404 {
		return nil, true, nil
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		return nil, false, fmt.Errorf("VM read returned HTTP %d; state retained", result.StatusCode())
	}
	if result.JSON200.Id != m.ID.ValueString() || result.JSON200.ProjectId != m.ProjectID.ValueString() {
		return nil, false, fmt.Errorf("API returned a mismatched VM identity; state retained")
	}
	return result.JSON200, false, nil
}
func (r *vmResource) wait(ctx context.Context, m *vmModel, deleting bool, initial *tatnet.V1VM) (*tatnet.V1VM, error) {
	timeout := r.operationTimeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	interval := r.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	vm := initial
	for {
		if !deleting && vm != nil {
			switch vm.Status {
			case "active", "running":
				return vm, nil
			case "error", "failed", "payment_pending", "deleting", "deleted":
				return vm, fmt.Errorf("VM %s is in status %q; its ID is retained in state", m.ID.ValueString(), vm.Status)
			}
		}
		if vm != nil {
			select {
			case <-ctx.Done():
				return vm, fmt.Errorf("timed out or cancelled waiting for VM %s; state retained", m.ID.ValueString())
			case <-time.After(interval):
			}
		}
		next, missing, err := r.get(ctx, m)
		if err != nil {
			return vm, err
		}
		if missing {
			if deleting {
				return nil, nil
			}
			return vm, fmt.Errorf("VM disappeared while waiting for creation; ID retained for reconciliation")
		}
		vm = next
	}
}
