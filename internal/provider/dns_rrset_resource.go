package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

type dnsRRsetResource struct{ client *tatnet.ClientWithResponses }
type dnsRRsetModel struct {
	ID       types.String `tfsdk:"id"`
	ZoneID   types.String `tfsdk:"zone_id"`
	Name     types.String `tfsdk:"name"`
	Type     types.String `tfsdk:"type"`
	TTL      types.Int64  `tfsdk:"ttl"`
	Records  types.Set    `tfsdk:"records"`
	Managed  types.Bool   `tfsdk:"managed"`
	Revision types.String `tfsdk:"revision"`
}

var _ resource.ResourceWithConfigure = (*dnsRRsetResource)(nil)
var _ resource.ResourceWithImportState = (*dnsRRsetResource)(nil)
var _ resource.ResourceWithValidateConfig = (*dnsRRsetResource)(nil)
var dnsRevision = regexp.MustCompile(`^[0-9a-f]{64}$`)

func NewDNSRRsetResource() resource.Resource { return &dnsRRsetResource{} }
func (*dnsRRsetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_rrset"
}
func (*dnsRRsetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "Own a complete DNS RRset and its shared TTL. Writes require the last observed revision; conflicts retain state. Never also manage its individual records.", Attributes: map[string]schema.Attribute{
		"id":       schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"zone_id":  schema.StringAttribute{Required: true, PlanModifiers: replace},
		"name":     schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Lowercase absolute name with trailing dot."},
		"type":     schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "A, AAAA, CNAME or TXT."},
		"ttl":      schema.Int64Attribute{Optional: true, Description: "Shared TTL: 0..2147483647. Omitted/null explicitly inherits the zone default."},
		"records":  schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "Complete set of 1..256 API presentation values. Changes and TTL changes commit atomically."},
		"managed":  schema.BoolAttribute{Computed: true},
		"revision": schema.StringAttribute{Computed: true, Description: "Opaque API snapshot revision used for conditional update/delete."},
	}}
}
func (r *dnsRRsetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
	}
}
func (*dnsRRsetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := strings.Split(req.ID, "/")
	if len(p) != 2 || !vpcUUID.MatchString(p[0]) || !vpcUUID.MatchString(p[1]) {
		resp.Diagnostics.AddError("Invalid DNS import ID", "Import using ZONE_UUID/RRSET_UUID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), p[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), p[1])...)
}
func rrsetValues(ctx context.Context, m dnsRRsetModel) ([]string, error) {
	var v []string
	if d := m.Records.ElementsAs(ctx, &v, false); d.HasError() {
		return nil, fmt.Errorf("records must be known strings")
	}
	return v, nil
}
func validateRRset(ctx context.Context, m dnsRRsetModel) error {
	if !m.TTL.IsUnknown() && !m.TTL.IsNull() && (m.TTL.ValueInt64() < 0 || m.TTL.ValueInt64() > 2147483647) {
		return fmt.Errorf("ttl must be null or 0..2147483647")
	}
	base := dnsRecordModel{ZoneID: m.ZoneID, Name: m.Name, Type: m.Type, Content: types.StringUnknown()}
	if e := validateDNSRecord(base); e != nil {
		return e
	}
	if m.Records.IsUnknown() {
		return nil
	}
	if m.Records.IsNull() || len(m.Records.Elements()) < 1 || len(m.Records.Elements()) > 256 {
		return fmt.Errorf("records must contain 1..256 values")
	}
	if m.Type.ValueString() == "CNAME" && len(m.Records.Elements()) != 1 {
		return fmt.Errorf("CNAME must have exactly one value")
	}
	for _, v := range m.Records.Elements() {
		base.Content = v.(types.String)
		if e := validateDNSRecord(base); e != nil {
			return e
		}
	}
	return nil
}
func (*dnsRRsetResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m dnsRRsetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := validateRRset(ctx, m); e != nil {
		resp.Diagnostics.AddError("Invalid RRset configuration", e.Error())
	}
}
func rrsetTTL(m dnsRRsetModel) *int {
	if m.TTL.IsNull() {
		return nil
	}
	v := int(m.TTL.ValueInt64())
	return &v
}
func validRRset(v *tatnet.V1DNSRRset, body []byte, m dnsRRsetModel) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	for _, key := range []string{"id", "zone_id", "name", "type", "ttl", "records", "managed", "revision"} {
		raw, ok := fields[key]
		if !ok || (string(raw) == "null" && key != "ttl") {
			return false
		}
	}
	if v == nil || !vpcUUID.MatchString(v.Id) || !strings.EqualFold(v.ZoneId, m.ZoneID.ValueString()) || (!m.ID.IsNull() && !m.ID.IsUnknown() && !strings.EqualFold(v.Id, m.ID.ValueString())) || !dnsAbsoluteName(v.Name) || v.Type == "" || !dnsRevision.MatchString(v.Revision) || len(v.Records) < 1 || len(v.Records) > 256 || (v.Ttl != nil && (*v.Ttl < 0 || *v.Ttl > 2147483647)) {
		return false
	}
	seen := map[string]bool{}
	for _, s := range v.Records {
		if s == "" || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
func (m *dnsRRsetModel) observeRRset(v *tatnet.V1DNSRRset) {
	if m.ID.IsNull() || m.ID.IsUnknown() {
		m.ID = types.StringValue(v.Id)
	}
	m.Name = types.StringValue(v.Name)
	m.Type = types.StringValue(v.Type)
	m.TTL = dnsTTL(v.Ttl)
	m.Managed = types.BoolValue(v.Managed)
	m.Revision = types.StringValue(v.Revision)
	values := make([]attr.Value, len(v.Records))
	for i, s := range v.Records {
		values[i] = types.StringValue(s)
	}
	m.Records = types.SetValueMust(types.StringType, values)
}
func (r *dnsRRsetResource) get(ctx context.Context, m dnsRRsetModel) (*tatnet.V1DNSRRset, error) {
	if r.client == nil || !dnsSelector(m.ID) || !dnsSelector(m.ZoneID) {
		return nil, fmt.Errorf("provider or RRset selector invalid; state retained")
	}
	res, e := r.client.DnsGetDnsRrsetWithResponse(ctx, m.ZoneID.ValueString(), m.ID.ValueString())
	if e != nil {
		return nil, fmt.Errorf("RRset read request failed; state retained")
	}
	if res.StatusCode() == 404 {
		return nil, errDNSRecordAbsent
	}
	if res.StatusCode() != 200 {
		return nil, fmt.Errorf("RRset read returned HTTP %d; state retained", res.StatusCode())
	}
	if !validRRset(res.JSON200, res.Body, m) {
		return nil, fmt.Errorf("RRset response invalid or mismatched; state retained")
	}
	return res.JSON200, nil
}
func rrsetMutationAllowed(v *tatnet.V1DNSRRset, m dnsRRsetModel) bool {
	return !v.Managed && v.Name == m.Name.ValueString() && v.Type == m.Type.ValueString() && dnsRevision.MatchString(m.Revision.ValueString()) && v.Revision == m.Revision.ValueString()
}
func rrsetPlanMatches(v *tatnet.V1DNSRRset, m dnsRRsetModel) bool {
	observed := m
	observed.observeRRset(v)
	return observed.Name.Equal(m.Name) && observed.Type.Equal(m.Type) && observed.TTL.Equal(m.TTL) && observed.Records.Equal(m.Records)
}
func (r *dnsRRsetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m dnsRRsetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := validateRRset(ctx, m); e != nil {
		resp.Diagnostics.AddError("Invalid RRset configuration", e.Error())
		return
	}
	values, e := rrsetValues(ctx, m)
	if e != nil || m.ZoneID.IsUnknown() || m.Name.IsUnknown() || m.Type.IsUnknown() || m.TTL.IsUnknown() || r.client == nil {
		resp.Diagnostics.AddError("Cannot create RRset", "Configure the provider and resolve every input before creation.")
		return
	}
	res, e := r.client.DnsCreateDnsRrsetWithResponse(ctx, m.ZoneID.ValueString(), tatnet.V1DNSRRsetCreate{Name: m.Name.ValueString(), Type: m.Type.ValueString(), Ttl: rrsetTTL(m), Records: values})
	if e != nil {
		resp.Diagnostics.AddError("Cannot create RRset", "Creation outcome unknown; inspect and explicitly import any created set before retrying. No automatic retry was made.")
		return
	}
	if res.StatusCode() != 201 || res.JSON201 == nil || !vpcUUID.MatchString(res.JSON201.Id) || !strings.EqualFold(res.JSON201.ZoneId, m.ZoneID.ValueString()) {
		resp.Diagnostics.AddError("Cannot create RRset", fmt.Sprintf("Create returned HTTP %d without a usable scoped ID; inspect the zone before retrying.", res.StatusCode()))
		return
	}
	v := res.JSON201
	m.ID = types.StringValue(v.Id)
	m.Managed = types.BoolValue(v.Managed)
	m.Revision = types.StringValue(v.Revision)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if !validRRset(v, res.Body, m) || v.Managed || !rrsetPlanMatches(v, m) {
		resp.Diagnostics.AddError("RRset creation mismatch", "Created ID retained; inspect the full set before retrying.")
		return
	}
	m.observeRRset(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *dnsRRsetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m dnsRRsetModel
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
		resp.Diagnostics.AddError("Cannot read RRset", e.Error())
		return
	}
	m.observeRRset(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *dnsRRsetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var old, m dnsRRsetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if e := validateRRset(ctx, m); e != nil {
		resp.Diagnostics.AddError("Invalid RRset configuration", e.Error())
		return
	}
	values, e := rrsetValues(ctx, m)
	if e != nil || m.TTL.IsUnknown() || !m.ZoneID.Equal(old.ZoneID) || !m.Name.Equal(old.Name) || !m.Type.Equal(old.Type) {
		resp.Diagnostics.AddError("RRset replacement required", "Zone/name/type changes require replacement; TTL and values must be known.")
		return
	}
	v, e := r.get(ctx, old)
	if e != nil {
		resp.Diagnostics.AddError("Cannot update RRset", e.Error())
		return
	}
	if !rrsetMutationAllowed(v, old) {
		resp.Diagnostics.AddError("RRset changed or protected", "Review the concurrent edit before replanning; state retained. Revision was not rebased.")
		return
	}
	res, e := r.client.DnsReplaceDnsRrsetWithResponse(ctx, old.ZoneID.ValueString(), old.ID.ValueString(), tatnet.V1DNSRRsetReplace{ExpectedRevision: old.Revision.ValueString(), Ttl: rrsetTTL(m), Records: values})
	if e != nil {
		resp.Diagnostics.AddError("Cannot update RRset", "Update outcome unknown; refresh before retrying; state retained.")
		return
	}
	m.ID = old.ID
	if res.StatusCode() != 200 || !validRRset(res.JSON200, res.Body, m) || res.JSON200.Managed || !rrsetPlanMatches(res.JSON200, m) {
		resp.Diagnostics.AddError("Cannot update RRset", fmt.Sprintf("Update returned HTTP %d or a mismatched response; state retained. Review conflicts before retrying.", res.StatusCode()))
		return
	}
	m.observeRRset(res.JSON200)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *dnsRRsetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m dnsRRsetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, e := r.get(ctx, m)
	if errors.Is(e, errDNSRecordAbsent) {
		return
	}
	if e != nil {
		resp.Diagnostics.AddError("Cannot delete RRset", e.Error())
		return
	}
	if !rrsetMutationAllowed(v, m) {
		resp.Diagnostics.AddError("RRset changed or protected", "Review the concurrent edit before replanning; state retained. Revision was not rebased.")
		return
	}
	res, e := r.client.DnsDeleteDnsRrsetWithResponse(ctx, m.ZoneID.ValueString(), m.ID.ValueString(), &tatnet.DnsDeleteDnsRrsetParams{ExpectedRevision: m.Revision.ValueString()})
	if e != nil {
		resp.Diagnostics.AddError("Cannot delete RRset", "Delete outcome unknown; refresh before retrying; state retained.")
		return
	}
	if res.StatusCode() != 204 && res.StatusCode() != 404 {
		resp.Diagnostics.AddError("Cannot delete RRset", fmt.Sprintf("Delete returned HTTP %d; state retained. Review conflicts before retrying.", res.StatusCode()))
	}
}
