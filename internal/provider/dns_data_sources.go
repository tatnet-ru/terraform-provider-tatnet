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

type dnsDataSource struct {
	client *tatnet.ClientWithResponses
	record bool
}
type dnsZoneModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Status        types.String `tfsdk:"status"`
	DefaultTTL    types.Int64  `tfsdk:"default_ttl"`
	DNSSECEnabled types.Bool   `tfsdk:"dnssec_enabled"`
}
type dnsRecordModel struct {
	ID      types.String `tfsdk:"id"`
	ZoneID  types.String `tfsdk:"zone_id"`
	Name    types.String `tfsdk:"name"`
	Type    types.String `tfsdk:"type"`
	Content types.String `tfsdk:"content"`
	TTL     types.Int64  `tfsdk:"ttl"`
	Managed types.Bool   `tfsdk:"managed"`
}

func NewDNSZoneDataSource() datasource.DataSource   { return &dnsDataSource{} }
func NewDNSRecordDataSource() datasource.DataSource { return &dnsDataSource{record: true} }
func (d *dnsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	suffix := "_dns_zone"
	if d.record {
		suffix = "_dns_record"
	}
	resp.TypeName = req.ProviderTypeName + suffix
}
func (d *dnsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id":   schema.StringAttribute{Required: true, Description: "Exact account-accessible UUID. Requires dns_zone:read on the zone."},
		"name": schema.StringAttribute{Computed: true, Description: "Absolute DNS name returned by the API, including the trailing dot."},
	}
	if d.record {
		attrs["zone_id"] = schema.StringAttribute{Required: true, Description: "Parent zone UUID; both identities are verified against the response."}
		attrs["type"] = schema.StringAttribute{Computed: true}
		attrs["content"] = schema.StringAttribute{Computed: true, Description: "API record content; no DNS queries or normalization are performed."}
		attrs["ttl"] = schema.Int64Attribute{Computed: true, Description: "RRset TTL, or null when inherited."}
		attrs["managed"] = schema.BoolAttribute{Computed: true, Description: "Whether the platform manages the RRset; null if omitted by the API."}
	} else {
		attrs["status"] = schema.StringAttribute{Computed: true, Description: "API zone status; this does not verify delegation or propagation."}
		attrs["default_ttl"] = schema.Int64Attribute{Computed: true}
		attrs["dnssec_enabled"] = schema.BoolAttribute{Computed: true}
	}
	resp.Schema = schema.Schema{Description: "Read DNS metadata by exact UUID without creating, adopting or changing records.", Attributes: attrs}
}
func (d *dnsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	d.client, ok = req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
	}
}
func dnsSelector(id types.String) bool {
	return !id.IsUnknown() && !id.IsNull() && vpcUUID.MatchString(id.ValueString())
}
func dnsAbsoluteName(name string) bool {
	return len(name) > 1 && strings.HasSuffix(name, ".") && !strings.ContainsAny(name, " \t\r\n")
}
func dnsTTL(ttl *int) types.Int64 {
	if ttl == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*ttl))
}
func (d *dnsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.record {
		d.readRecord(ctx, req, resp)
		return
	}
	var m dnsZoneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !dnsSelector(m.ID) {
		resp.Diagnostics.AddError("Invalid DNS zone ID", "id must be a known zone UUID.")
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the TatNet provider before reading DNS.")
		return
	}
	r, err := d.client.DnsGetZoneWithResponse(ctx, m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read DNS zone", "DNS request failed; check connectivity and API availability.")
		return
	}
	if r.StatusCode() != 200 {
		resp.Diagnostics.AddError("Cannot read DNS zone", fmt.Sprintf("DNS zone returned HTTP %d; check the ID, account access and dns_zone:read permission.", r.StatusCode()))
		return
	}
	z := r.JSON200
	if z == nil || !strings.EqualFold(z.Id, m.ID.ValueString()) || !dnsAbsoluteName(z.Name) || strings.TrimSpace(z.Status) == "" || (z.DefaultTtl != nil && *z.DefaultTtl < 0) {
		resp.Diagnostics.AddError("Cannot read DNS zone", "DNS zone returned an invalid or mismatched response.")
		return
	}
	m.Name = types.StringValue(z.Name)
	m.Status = types.StringValue(z.Status)
	m.DefaultTTL = dnsTTL(z.DefaultTtl)
	m.DNSSECEnabled = types.BoolPointerValue(z.DnssecEnabled)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (d *dnsDataSource) readRecord(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m dnsRecordModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !dnsSelector(m.ID) || !dnsSelector(m.ZoneID) {
		resp.Diagnostics.AddError("Invalid DNS record selector", "id and zone_id must be known UUIDs.")
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure the TatNet provider before reading DNS.")
		return
	}
	r, err := d.client.DnsGetDnsRecordWithResponse(ctx, m.ZoneID.ValueString(), m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read DNS record", "DNS request failed; check connectivity and API availability.")
		return
	}
	if r.StatusCode() != 200 {
		resp.Diagnostics.AddError("Cannot read DNS record", fmt.Sprintf("DNS record returned HTTP %d; check both IDs, account access and dns_zone:read permission.", r.StatusCode()))
		return
	}
	v := r.JSON200
	if v == nil || !strings.EqualFold(v.Id, m.ID.ValueString()) || !strings.EqualFold(v.ZoneId, m.ZoneID.ValueString()) || !dnsAbsoluteName(v.Name) || strings.TrimSpace(v.Type) == "" || (v.Ttl != nil && *v.Ttl < 0) {
		resp.Diagnostics.AddError("Cannot read DNS record", "DNS record returned an invalid or mismatched response.")
		return
	}
	m.Name = types.StringValue(v.Name)
	m.Type = types.StringValue(v.Type)
	m.Content = types.StringValue(v.Content)
	m.TTL = dnsTTL(v.Ttl)
	m.Managed = types.BoolPointerValue(v.Managed)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
