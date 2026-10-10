package provider

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

type dnsRecordResource struct{ client *tatnet.ClientWithResponses }

var _ resource.ResourceWithConfigure = (*dnsRecordResource)(nil)
var _ resource.ResourceWithValidateConfig = (*dnsRecordResource)(nil)
var _ resource.ResourceWithImportState = (*dnsRecordResource)(nil)
var errDNSRecordAbsent = errors.New("DNS record not found")

func NewDNSRecordResource() resource.Resource { return &dnsRecordResource{} }
func (*dnsRecordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record"
}
func (*dnsRecordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "Manage one non-platform-managed DNS record. Only content changes in place. TTL belongs to the shared RRset and is never written by this resource. API success does not confirm DNS propagation.", Attributes: map[string]schema.Attribute{
		"id":      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"zone_id": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Parent zone UUID. Requires dns_zone:read and dns_zone:write."},
		"name":    schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Lowercase absolute DNS name, including trailing dot. Changes replace the record."},
		"type":    schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "A, AAAA, CNAME or TXT. Changes replace the record."},
		"content": schema.StringAttribute{Required: true, Description: "Record content without surrounding whitespace. CNAME targets must be lowercase absolute names. TXT uses API presentation format."},
		"ttl":     schema.Int64Attribute{Computed: true, Description: "Shared RRset TTL; null means zone default. Never sent in a write request."},
		"managed": schema.BoolAttribute{Computed: true, Description: "Platform ownership flag. Mutations require explicit false."},
	}}
}
func validateDNSRecord(m dnsRecordModel) error {
	if !m.ZoneID.IsUnknown() && !dnsSelector(m.ZoneID) {
		return fmt.Errorf("zone_id must be a zone UUID")
	}
	if !m.Name.IsUnknown() && (!dnsAbsoluteName(m.Name.ValueString()) || strings.ToLower(m.Name.ValueString()) != m.Name.ValueString() || strings.Contains(m.Name.ValueString(), "..")) {
		return fmt.Errorf("name must be a lowercase absolute DNS name without empty labels")
	}
	if !m.Type.IsUnknown() {
		switch m.Type.ValueString() {
		case "A", "AAAA", "CNAME", "TXT":
		default:
			return fmt.Errorf("type must be A, AAAA, CNAME or TXT")
		}
	}
	if !m.Content.IsUnknown() {
		s := m.Content.ValueString()
		if s == "" || strings.TrimSpace(s) != s || strings.ContainsAny(s, "\r\n\x00") {
			return fmt.Errorf("content must be nonempty without surrounding whitespace, newlines or NUL")
		}
		switch m.Type.ValueString() {
		case "A", "AAAA":
			a, e := netip.ParseAddr(s)
			if e != nil || a.Zone() != "" || (m.Type.ValueString() == "A" && !a.Is4()) || (m.Type.ValueString() == "AAAA" && (!a.Is6() || a.Is4In6())) {
				return fmt.Errorf("content must match the address type")
			}
		case "CNAME":
			if !dnsAbsoluteName(s) || strings.ToLower(s) != s {
				return fmt.Errorf("CNAME content must be a lowercase absolute DNS name")
			}
		}
	}
	return nil
}
func (*dnsRecordResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m dnsRecordModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := validateDNSRecord(m); e != nil {
		resp.Diagnostics.AddError("Invalid DNS record configuration", e.Error())
	}
}
func (r *dnsRecordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
	}
}
func (*dnsRecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := strings.Split(req.ID, "/")
	if len(p) != 2 || !vpcUUID.MatchString(p[0]) || !vpcUUID.MatchString(p[1]) {
		resp.Diagnostics.AddError("Invalid DNS import ID", "Import using ZONE_UUID/RECORD_UUID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), p[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), p[1])...)
}
func validDNSObservation(v *tatnet.V1DNSRecord, m dnsRecordModel) bool {
	return v != nil && vpcUUID.MatchString(v.Id) && strings.EqualFold(v.ZoneId, m.ZoneID.ValueString()) && (m.ID.IsNull() || m.ID.IsUnknown() || strings.EqualFold(v.Id, m.ID.ValueString())) && dnsAbsoluteName(v.Name) && v.Type != "" && v.Content != "" && (v.Ttl == nil || *v.Ttl >= 0)
}
func (m *dnsRecordModel) observeDNS(v *tatnet.V1DNSRecord) {
	if m.ID.IsNull() || m.ID.IsUnknown() {
		m.ID = types.StringValue(v.Id)
	}
	m.Name = types.StringValue(v.Name)
	m.Type = types.StringValue(v.Type)
	m.Content = types.StringValue(v.Content)
	m.TTL = dnsTTL(v.Ttl)
	m.Managed = types.BoolPointerValue(v.Managed)
}
func (r *dnsRecordResource) get(ctx context.Context, m dnsRecordModel) (*tatnet.V1DNSRecord, error) {
	if r.client == nil || !dnsSelector(m.ID) || !dnsSelector(m.ZoneID) {
		return nil, fmt.Errorf("provider or DNS selector invalid; state retained")
	}
	res, e := r.client.DnsGetDnsRecordWithResponse(ctx, m.ZoneID.ValueString(), m.ID.ValueString())
	if e != nil {
		return nil, fmt.Errorf("DNS request failed; state retained")
	}
	if res.StatusCode() == 404 {
		return nil, errDNSRecordAbsent
	}
	if res.StatusCode() != 200 {
		return nil, fmt.Errorf("DNS read returned HTTP %d; state retained", res.StatusCode())
	}
	if !validDNSObservation(res.JSON200, m) {
		return nil, fmt.Errorf("DNS response invalid or mismatched; state retained")
	}
	return res.JSON200, nil
}
func dnsMutationAllowed(v *tatnet.V1DNSRecord, m dnsRecordModel) bool {
	return v.Managed != nil && !*v.Managed && v.Name == m.Name.ValueString() && v.Type == m.Type.ValueString()
}
func (r *dnsRecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := validateDNSRecord(m); e != nil {
		resp.Diagnostics.AddError("Invalid DNS configuration", e.Error())
		return
	}
	if m.ZoneID.IsUnknown() || m.Name.IsUnknown() || m.Type.IsUnknown() || m.Content.IsUnknown() || r.client == nil {
		resp.Diagnostics.AddError("Cannot create DNS record", "Configure the provider and resolve all inputs before creation.")
		return
	}
	// Omit TTL: supplying it would change the TTL of existing sibling records.
	res, e := r.client.DnsCreateDnsRecordWithResponse(ctx, m.ZoneID.ValueString(), tatnet.V1DNSRecordCreate{Name: m.Name.ValueString(), Type: m.Type.ValueString(), Content: m.Content.ValueString()})
	if e != nil {
		resp.Diagnostics.AddError("Cannot create DNS record", "Creation outcome unknown; inspect the zone and import any created record before retrying. No automatic retry was made.")
		return
	}
	if res.StatusCode() != 201 || res.JSON201 == nil || !vpcUUID.MatchString(res.JSON201.Id) || !strings.EqualFold(res.JSON201.ZoneId, m.ZoneID.ValueString()) {
		resp.Diagnostics.AddError("Cannot create DNS record", fmt.Sprintf("Creation returned HTTP %d without a scoped usable ID; inspect the zone before retrying.", res.StatusCode()))
		return
	}
	v := res.JSON201
	m.ID = types.StringValue(v.Id)
	m.TTL = dnsTTL(v.Ttl)
	m.Managed = types.BoolPointerValue(v.Managed)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if !validDNSObservation(v, m) || !dnsMutationAllowed(v, m) || v.Content != m.Content.ValueString() {
		resp.Diagnostics.AddError("DNS creation mismatch", "Created ID retained; inspect the record before retrying.")
	}
}
func (r *dnsRecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.get(ctx, m)
	if errors.Is(e, errDNSRecordAbsent) {
		resp.State.RemoveResource(ctx)
		return
	}
	if e != nil {
		resp.Diagnostics.AddError("Cannot read DNS record", e.Error())
		return
	}
	m.observeDNS(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *dnsRecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var old, m dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := validateDNSRecord(m); e != nil {
		resp.Diagnostics.AddError("Invalid DNS configuration", e.Error())
		return
	}
	if m.ZoneID != old.ZoneID || m.Name != old.Name || m.Type != old.Type || m.Content.IsUnknown() {
		resp.Diagnostics.AddError("DNS replacement required", "Zone, name and type changes require replacement; content must be known.")
		return
	}
	v, e := r.get(ctx, old)
	if e != nil {
		resp.Diagnostics.AddError("Cannot update DNS record", e.Error()+"; refresh before retrying")
		return
	}
	if !dnsMutationAllowed(v, old) {
		resp.Diagnostics.AddError("Cannot update DNS record", "Ownership or name/type changed; state retained.")
		return
	}
	content := m.Content.ValueString()
	res, e := r.client.DnsUpdateDnsRecordWithResponse(ctx, old.ZoneID.ValueString(), old.ID.ValueString(), tatnet.V1DNSRecordUpdate{Content: &content})
	if e != nil {
		resp.Diagnostics.AddError("Cannot update DNS record", "Update outcome unknown; refresh before retrying; state retained.")
		return
	}
	m.ID = old.ID
	if res.StatusCode() != 200 || !validDNSObservation(res.JSON200, m) || !dnsMutationAllowed(res.JSON200, m) || res.JSON200.Content != content {
		resp.Diagnostics.AddError("Cannot update DNS record", fmt.Sprintf("Update returned HTTP %d or a mismatched response; state retained.", res.StatusCode()))
		return
	}
	m.observeDNS(res.JSON200)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *dnsRecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.get(ctx, m)
	if errors.Is(e, errDNSRecordAbsent) {
		return
	}
	if e != nil {
		resp.Diagnostics.AddError("Cannot delete DNS record", e.Error())
		return
	}
	if !dnsMutationAllowed(v, m) {
		resp.Diagnostics.AddError("Cannot delete DNS record", "Ownership or name/type changed; state retained.")
		return
	}
	res, e := r.client.DnsDeleteDnsRecordWithResponse(ctx, m.ZoneID.ValueString(), m.ID.ValueString())
	if e != nil {
		resp.Diagnostics.AddError("Cannot delete DNS record", "Delete outcome unknown; refresh before retrying; state retained.")
		return
	}
	if res.StatusCode() != 204 && res.StatusCode() != 404 {
		resp.Diagnostics.AddError("Cannot delete DNS record", fmt.Sprintf("Delete returned HTTP %d; state retained.", res.StatusCode()))
	}
}
