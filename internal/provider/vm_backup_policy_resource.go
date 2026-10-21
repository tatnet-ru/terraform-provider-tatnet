package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

type vmBackupPolicyResource struct{ client *tatnet.ClientWithResponses }
type vmBackupPolicyModel struct {
	ID             types.String `tfsdk:"id"`
	ProjectID      types.String `tfsdk:"project_id"`
	VMID           types.String `tfsdk:"vm_id"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	HourUTC        types.Int64  `tfsdk:"hour_utc"`
	RetentionDays  types.Int64  `tfsdk:"retention_days"`
	NextRunAt      types.String `tfsdk:"next_run_at"`
	StorageBytes   types.Int64  `tfsdk:"storage_bytes"`
	PricePerGBHour types.String `tfsdk:"price_per_gb_hour"`
}

var _ resource.ResourceWithConfigure = (*vmBackupPolicyResource)(nil)
var _ resource.ResourceWithImportState = (*vmBackupPolicyResource)(nil)
var _ resource.ResourceWithValidateConfig = (*vmBackupPolicyResource)(nil)

func NewVMBackupPolicyResource() resource.Resource { return &vmBackupPolicyResource{} }
func (*vmBackupPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_backup_policy"
}
func (*vmBackupPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	identity := func() schema.StringAttribute {
		return schema.StringAttribute{Required: true, Description: "UUID.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}}
	}
	resp.Schema = schema.Schema{Description: "Daily VM backup policy. Storage is billed by actual compressed GB-hours. Destroy disables future backups and preserves existing recovery points; it does not delete their stored data or end their storage charges. Retention changes affect future automatic copies only; the last good copy is kept until a newer one exists.", Attributes: map[string]schema.Attribute{
		"id":         schema.StringAttribute{Computed: true, Description: "project_id/vm_id.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"project_id": identity(), "vm_id": identity(),
		"enabled":           schema.BoolAttribute{Required: true, Description: "Enable daily automatic backups."},
		"hour_utc":          schema.Int64Attribute{Required: true, Description: "Daily UTC hour, 0–23."},
		"retention_days":    schema.Int64Attribute{Required: true, Description: "Retention for future automatic backups, 1–365 days."},
		"next_run_at":       schema.StringAttribute{Computed: true, Description: "Next scheduled capture, RFC3339; null when disabled."},
		"storage_bytes":     schema.Int64Attribute{Computed: true, Description: "Currently stored compressed bytes."},
		"price_per_gb_hour": schema.StringAttribute{Computed: true, Description: "Current hourly price per decimal GB."},
	}}
}
func (*vmBackupPolicyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m vmBackupPolicyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for k, v := range map[string]types.String{"project_id": m.ProjectID, "vm_id": m.VMID} {
		if !v.IsNull() && !v.IsUnknown() {
			if _, err := uuid.Parse(v.ValueString()); err != nil {
				resp.Diagnostics.AddAttributeError(path.Root(k), "Invalid UUID", k+" must be a UUID.")
			}
		}
	}
	if !m.HourUTC.IsUnknown() && !m.HourUTC.IsNull() && (m.HourUTC.ValueInt64() < 0 || m.HourUTC.ValueInt64() > 23) {
		resp.Diagnostics.AddAttributeError(path.Root("hour_utc"), "Invalid UTC hour", "Use an hour from 0 to 23.")
	}
	if !m.RetentionDays.IsUnknown() && !m.RetentionDays.IsNull() && (m.RetentionDays.ValueInt64() < 1 || m.RetentionDays.ValueInt64() > 365) {
		resp.Diagnostics.AddAttributeError(path.Root("retention_days"), "Invalid retention", "Use 1 to 365 days.")
	}
}
func (r *vmBackupPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
	}
}
func (r *vmBackupPolicyResource) put(ctx context.Context, m *vmBackupPolicyModel) error {
	if r.client == nil {
		return fmt.Errorf("provider is not configured")
	}
	enabled, hour, days := m.Enabled.ValueBool(), int(m.HourUTC.ValueInt64()), int(m.RetentionDays.ValueInt64())
	result, err := r.client.VmsSetBackupPolicyWithResponse(ctx, m.ProjectID.ValueString(), m.VMID.ValueString(), tatnet.VMBackupPolicyRequest{Enabled: &enabled, HourUtc: &hour, RetentionDays: &days})
	if err != nil {
		return fmt.Errorf("policy write request failed; check the VM policy before retrying")
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		return fmt.Errorf("policy write returned HTTP %d; state retained", result.StatusCode())
	}
	if err = m.observe(result.JSON200); err != nil {
		return err
	}
	// Never silently accept a server normalization of configured settings.
	if m.Enabled.ValueBool() != enabled || int(m.HourUTC.ValueInt64()) != hour || int(m.RetentionDays.ValueInt64()) != days {
		return fmt.Errorf("API returned policy settings different from the requested values")
	}
	return nil
}
func (m *vmBackupPolicyModel) observe(p *tatnet.VMBackupPolicyResponse) error {
	if p.Enabled == nil || p.HourUtc == nil || p.RetentionDays == nil || *p.HourUtc < 0 || *p.HourUtc > 23 || *p.RetentionDays < 1 || *p.RetentionDays > 365 || p.StorageBytes == nil || *p.StorageBytes < 0 {
		return fmt.Errorf("API returned an invalid backup policy; state retained")
	}
	m.ID = types.StringValue(m.ProjectID.ValueString() + "/" + m.VMID.ValueString())
	m.Enabled = types.BoolValue(*p.Enabled)
	m.HourUTC = types.Int64Value(int64(*p.HourUtc))
	m.RetentionDays = types.Int64Value(int64(*p.RetentionDays))
	m.StorageBytes = types.Int64Value(int64(*p.StorageBytes))
	m.NextRunAt = types.StringPointerValue(p.NextRunAt)
	m.PricePerGBHour = types.StringPointerValue(p.PricePerGbHour)
	return nil
}
func (r *vmBackupPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m vmBackupPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Cannot configure VM backup policy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmBackupPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m vmBackupPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Cannot update VM backup policy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmBackupPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m vmBackupPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure TatNet before reading a policy.")
		return
	}
	result, err := r.client.VmsGetBackupPolicyWithResponse(ctx, m.ProjectID.ValueString(), m.VMID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read backup policy", "Policy read request failed; state retained.")
		return
	}
	if result.StatusCode() == 404 {
		resp.State.RemoveResource(ctx)
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError("Cannot read backup policy", fmt.Sprintf("API returned HTTP %d; state retained.", result.StatusCode()))
		return
	}
	if err = m.observe(result.JSON200); err != nil {
		resp.Diagnostics.AddError("Invalid policy response", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmBackupPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m vmBackupPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure TatNet before disabling a policy.")
		return
	}
	// PUT applies defaults to omitted fields. Read the current policy before
	// disabling so external schedule/retention edits are preserved.
	current, err := r.client.VmsGetBackupPolicyWithResponse(ctx, m.ProjectID.ValueString(), m.VMID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot disable backup policy", "Policy read request failed; state retained.")
		return
	}
	if current.StatusCode() == 404 {
		resp.State.RemoveResource(ctx)
		return
	}
	if current.StatusCode() != 200 || current.JSON200 == nil {
		resp.Diagnostics.AddError("Cannot disable backup policy", fmt.Sprintf("Policy read returned HTTP %d; state retained.", current.StatusCode()))
		return
	}
	if err = m.observe(current.JSON200); err != nil {
		resp.Diagnostics.AddError("Cannot disable backup policy", err.Error())
		return
	}
	disabled, hour, days := false, int(m.HourUTC.ValueInt64()), int(m.RetentionDays.ValueInt64())
	result, err := r.client.VmsSetBackupPolicyWithResponse(ctx, m.ProjectID.ValueString(), m.VMID.ValueString(), tatnet.VMBackupPolicyRequest{Enabled: &disabled, HourUtc: &hour, RetentionDays: &days})
	if err != nil {
		resp.Diagnostics.AddError("Cannot disable backup policy", "Policy write request failed; state retained.")
		return
	}
	if result.StatusCode() == 404 {
		resp.State.RemoveResource(ctx)
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil || result.JSON200.Enabled == nil || *result.JSON200.Enabled {
		resp.Diagnostics.AddError("Cannot disable backup policy", fmt.Sprintf("API returned HTTP %d without confirming disabled; state retained.", result.StatusCode()))
		return
	}
	resp.State.RemoveResource(ctx)
}
func (*vmBackupPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 {
		resp.Diagnostics.AddError("Invalid import ID", "Use project_id/vm_id.")
		return
	}
	for _, p := range parts {
		if _, err := uuid.Parse(p); err != nil {
			resp.Diagnostics.AddError("Invalid import ID", "Use project_id/vm_id with two UUIDs.")
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), parts[1])...)
}
