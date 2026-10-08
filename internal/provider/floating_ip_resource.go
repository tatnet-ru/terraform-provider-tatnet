package provider

import (
	"context"
	"fmt"
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

type floatingIPResource struct {
	client                         *tatnet.ClientWithResponses
	pollInterval, operationTimeout time.Duration
}
type floatingIPModel struct {
	ID          types.String `tfsdk:"id"`
	ClusterID   types.String `tfsdk:"cluster_id"`
	Name        types.String `tfsdk:"name"`
	InterfaceID types.String `tfsdk:"vm_interface_id"`
	Address     types.String `tfsdk:"address"`
	Status      types.String `tfsdk:"status"`
}

var _ resource.ResourceWithConfigure = (*floatingIPResource)(nil)
var _ resource.ResourceWithValidateConfig = (*floatingIPResource)(nil)
var _ resource.ResourceWithImportState = (*floatingIPResource)(nil)

func NewFloatingIPResource() resource.Resource {
	return &floatingIPResource{pollInterval: 5 * time.Second, operationTimeout: 10 * time.Minute}
}
func (*floatingIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_floating_ip"
}
func (*floatingIPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Account-scoped public IPv4 address. Billed until released, including while detached. Attachment changes preserve the address; region changes replace it. Operations wait up to ten minutes.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"cluster_id":      schema.StringAttribute{Required: true, Description: "Region UUID. Changes replace the address.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"name":            schema.StringAttribute{Optional: true, Description: "Optional label (1–255 characters). Removing it clears the label."},
		"vm_interface_id": schema.StringAttribute{Optional: true, Description: "VM interface UUID in the same region. Omit to keep the address detached. Changes detach, wait, then attach without releasing the address."},
		"address":         schema.StringAttribute{Computed: true, Description: "Allocated public IPv4 address."},
		"status":          schema.StringAttribute{Computed: true, Description: "Observed networking status."},
	}}
}
func (*floatingIPResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m floatingIPModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, v := range map[string]types.String{"cluster_id": m.ClusterID, "name": m.Name, "vm_interface_id": m.InterfaceID} {
		if !v.IsNull() && !v.IsUnknown() && strings.TrimSpace(v.ValueString()) == "" {
			resp.Diagnostics.AddError("Invalid floating IP configuration", name+" must not be empty.")
		}
	}
	if !m.Name.IsUnknown() && utf8.RuneCountInString(m.Name.ValueString()) > 255 {
		resp.Diagnostics.AddError("Invalid floating IP name", "name must contain at most 255 characters.")
	}
}
func (r *floatingIPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
	}
}
func (*floatingIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) == "" || strings.ContainsAny(req.ID, "/\\") {
		resp.Diagnostics.AddError("Invalid floating IP ID", "Import using the address UUID, not the IP address or project/ID.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
func (r *floatingIPResource) timeout(ctx context.Context) (context.Context, context.CancelFunc) {
	d := r.operationTimeout
	if d <= 0 {
		d = 10 * time.Minute
	}
	return context.WithTimeout(ctx, d)
}
func (m *floatingIPModel) observe(f *tatnet.V1FloatingIP) {
	m.ID = types.StringValue(f.Id)
	m.ClusterID = types.StringValue(f.ClusterId)
	m.Name = types.StringPointerValue(f.Name)
	m.InterfaceID = types.StringPointerValue(f.VmInterfaceId)
	m.Address = types.StringValue(f.Address)
	m.Status = types.StringValue(f.Status)
}

// Never return API bodies or last_error: they may include private backend data.
func checkFloatingIP(f *tatnet.V1FloatingIP, id string) error {
	if f == nil || f.Id == "" || (id != "" && f.Id != id) || f.Address == "" || f.ClusterId == "" {
		return fmt.Errorf("API returned an incomplete or mismatched floating IP; state retained")
	}
	if f.TargetKind != nil && *f.TargetKind != "interface" {
		return fmt.Errorf("VPC NAT addresses are not supported by this resource; state retained")
	}
	if f.AutoRelease != nil && *f.AutoRelease {
		return fmt.Errorf("automatically released addresses cannot be managed safely; manually take ownership before importing; state retained")
	}
	return nil
}
func (r *floatingIPResource) get(ctx context.Context, id string) (*tatnet.V1FloatingIP, error) {
	if r.client == nil {
		return nil, fmt.Errorf("provider is not configured")
	}
	res, err := r.client.NetworkingGetFloatingIpWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("floating IP read request failed; state retained")
	}
	if res.StatusCode() == 404 {
		return nil, nil
	}
	if res.StatusCode() != 200 {
		return nil, fmt.Errorf("floating IP read returned HTTP %d; state retained", res.StatusCode())
	}
	if err = checkFloatingIP(res.JSON200, id); err != nil {
		return nil, err
	}
	return res.JSON200, nil
}
func (r *floatingIPResource) wait(ctx context.Context, f *tatnet.V1FloatingIP, want string) (*tatnet.V1FloatingIP, error) {
	interval := r.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		if f.Status == want {
			return f, nil
		}
		if f.Status == "error" {
			return f, fmt.Errorf("floating IP networking operation failed; ID retained for reconciliation")
		}
		select {
		case <-ctx.Done():
			return f, fmt.Errorf("timed out or cancelled waiting for floating IP; state retained")
		case <-time.After(interval):
		}
		next, err := r.get(ctx, f.Id)
		if err != nil {
			return f, err
		}
		if next == nil {
			return f, fmt.Errorf("floating IP disappeared while waiting; reconcile state before retrying")
		}
		f = next
	}
}
func (r *floatingIPResource) detach(ctx context.Context, f *tatnet.V1FloatingIP) (*tatnet.V1FloatingIP, error) {
	if f.VmInterfaceId != nil {
		res, err := r.client.NetworkingDetachFloatingIpWithResponse(ctx, f.Id)
		if err != nil {
			return f, fmt.Errorf("detach outcome unknown; refresh before retrying")
		}
		if res.StatusCode() != 200 {
			return f, fmt.Errorf("detach returned HTTP %d; state retained", res.StatusCode())
		}
		if err = checkFloatingIP(res.JSON200, f.Id); err != nil {
			return f, err
		}
		f = res.JSON200
	}
	return r.wait(ctx, f, "available")
}
func (r *floatingIPResource) reconcile(ctx context.Context, f *tatnet.V1FloatingIP, want floatingIPModel) (*tatnet.V1FloatingIP, error) {
	if !types.StringPointerValue(f.Name).Equal(want.Name) {
		res, err := r.client.NetworkingUpdateFloatingIpWithResponse(ctx, f.Id, tatnet.V1FloatingIPUpdate{Name: want.Name.ValueStringPointer()})
		if err != nil {
			return f, fmt.Errorf("rename outcome unknown; refresh before retrying")
		}
		if res.StatusCode() != 200 {
			return f, fmt.Errorf("rename returned HTTP %d; state retained", res.StatusCode())
		}
		if err = checkFloatingIP(res.JSON200, f.Id); err != nil {
			return f, err
		}
		f = res.JSON200
	}
	var err error
	if !types.StringPointerValue(f.VmInterfaceId).Equal(want.InterfaceID) {
		f, err = r.detach(ctx, f)
		if err != nil {
			return f, err
		}
		if !want.InterfaceID.IsNull() {
			res, e := r.client.NetworkingAttachFloatingIpWithResponse(ctx, f.Id, tatnet.V1FloatingIPAttach{VmInterfaceId: want.InterfaceID.ValueString()})
			if e != nil {
				return f, fmt.Errorf("attach outcome unknown; refresh before retrying")
			}
			if res.StatusCode() != 200 {
				return f, fmt.Errorf("attach returned HTTP %d; state retained", res.StatusCode())
			}
			if e = checkFloatingIP(res.JSON200, f.Id); e != nil {
				return f, e
			}
			f = res.JSON200
		}
	}
	status := "available"
	if !want.InterfaceID.IsNull() {
		status = "attached"
	}
	f, err = r.wait(ctx, f, status)
	if err == nil && (!types.StringPointerValue(f.VmInterfaceId).Equal(want.InterfaceID) || !types.StringPointerValue(f.Name).Equal(want.Name) || f.ClusterId != want.ClusterID.ValueString()) {
		err = fmt.Errorf("floating IP changed concurrently or API did not apply requested configuration; state retained")
	}
	return f, err
}
func (r *floatingIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m floatingIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure TatNet before allocating an address.")
		return
	}
	ctx, cancel := r.timeout(ctx)
	defer cancel()
	res, err := r.client.NetworkingCreateFloatingIpWithResponse(ctx, tatnet.V1FloatingIPCreate{ClusterId: m.ClusterID.ValueString(), Name: m.Name.ValueStringPointer()})
	if err != nil {
		resp.Diagnostics.AddError("Floating IP allocation outcome unknown", "Check the account before retrying. The API has no idempotent allocation token; the request was not retried.")
		return
	}
	if res.StatusCode() != 201 || res.JSON201 == nil || res.JSON201.Id == "" {
		resp.Diagnostics.AddError("Cannot allocate floating IP", fmt.Sprintf("API returned HTTP %d without a usable allocation. Check the account before retrying.", res.StatusCode()))
		return
	}
	want := m
	m.observe(res.JSON201)
	// Retain the allocation even if validation or subsequent attachment fails.
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if err = checkFloatingIP(res.JSON201, ""); err == nil && res.JSON201.ClusterId != want.ClusterID.ValueString() {
		err = fmt.Errorf("allocated floating IP is in the wrong region; ID retained")
	}
	if err == nil {
		var f *tatnet.V1FloatingIP
		f, err = r.reconcile(ctx, res.JSON201, want)
		m.observe(f)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Floating IP did not become ready", err.Error())
	}
}
func (r *floatingIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m floatingIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	f, err := r.get(ctx, m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read floating IP", err.Error())
		return
	}
	if f == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	m.observe(f)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *floatingIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var want, old floatingIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &want)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := r.timeout(ctx)
	defer cancel()
	f, err := r.get(ctx, old.ID.ValueString())
	if err == nil && f == nil {
		err = fmt.Errorf("floating IP disappeared; refresh before retrying")
	}
	if err == nil && f.ClusterId != want.ClusterID.ValueString() {
		err = fmt.Errorf("region changes require replacement")
	}
	if err == nil {
		f, err = r.reconcile(ctx, f, want)
	}
	if f != nil {
		old.observe(f)
		resp.Diagnostics.Append(resp.State.Set(ctx, &old)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot update floating IP", err.Error())
	}
}
func (r *floatingIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m floatingIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := r.timeout(ctx)
	defer cancel()
	f, err := r.get(ctx, m.ID.ValueString())
	if err == nil && f == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	if err == nil {
		f, err = r.detach(ctx, f)
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot release floating IP", err.Error())
		return
	}
	res, err := r.client.NetworkingDeleteFloatingIpWithResponse(ctx, f.Id)
	if err != nil {
		resp.Diagnostics.AddError("Release outcome unknown", "Refresh before retrying; allocation remains tracked.")
		return
	}
	if res.StatusCode() != 204 && res.StatusCode() != 404 {
		resp.Diagnostics.AddError("Cannot release floating IP", fmt.Sprintf("API returned HTTP %d; allocation remains tracked.", res.StatusCode()))
		return
	}
	resp.State.RemoveResource(ctx)
}
