package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

const dnsZoneBody = `{"id":"11111111-1111-4111-8111-111111111111","name":"example.com.","status":"active","default_ttl":300,"dnssec_enabled":false}`
const dnsRecordBody = `{"id":"22222222-2222-4222-8222-222222222222","zone_id":"11111111-1111-4111-8111-111111111111","name":"pilot.example.com.","type":"A","content":"192.0.2.10","ttl":300,"managed":false}`

func dnsRead(t *testing.T, d *dnsDataSource, id, zone interface{}) datasource.ReadResponse {
	t.Helper()
	ctx := context.Background()
	var s datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &s)
	values := map[string]tftypes.Value{}
	for k, typ := range s.Schema.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes {
		values[k] = tftypes.NewValue(typ, nil)
	}
	values["id"] = tftypes.NewValue(tftypes.String, id)
	if d.record {
		values["zone_id"] = tftypes.NewValue(tftypes.String, zone)
	}
	config := tfsdk.Config{Raw: tftypes.NewValue(s.Schema.Type().TerraformType(ctx), values), Schema: s.Schema}
	r := datasource.ReadResponse{State: tfsdk.State{Schema: s.Schema}}
	d.Read(ctx, datasource.ReadRequest{Config: config}, &r)
	return r
}

func TestDNSRead(t *testing.T) {
	for _, record := range []bool{false, true} {
		name := "zone"
		body := dnsZoneBody
		id := testVPCID
		if record {
			name = "record"
			body = dnsRecordBody
			id = testRegionID
		}
		cases := []struct {
			name, body string
			code       int
			bad        bool
		}{
			{"complete", body, 200, false},
			{"optional omitted", strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(body, `,"ttl":300`, ""), `,"managed":false`, ""), `,"default_ttl":300`, ""), `,"dnssec_enabled":false`, ""), 200, false},
			{"wrong identity", strings.Replace(body, `"id":"`+id+`"`, `"id":"33333333-3333-4333-8333-333333333333"`, 1), 200, true},
			{"relative name", strings.ReplaceAll(body, "example.com.", "example.com"), 200, true},
			{"empty", `{}`, 200, true},
			{"null", `null`, 200, true},
			{"malformed", `{`, 200, true},
			{"unauthorized", `{"detail":"secret marker"}`, 401, true},
			{"forbidden", `{"detail":"secret marker"}`, 403, true},
			{"absent", `{"detail":"secret marker"}`, 404, true},
			{"server error", `{"detail":"secret marker"}`, 500, true},
			{"negative ttl", strings.ReplaceAll(body, ":300", ":-1"), 200, true},
		}
		if record {
			cases = append(cases, struct {
				name, body string
				code       int
				bad        bool
			}{"wrong parent", strings.Replace(body, testVPCID, testNATID, 1), 200, true})
			cases = append(cases, struct {
				name, body string
				code       int
				bad        bool
			}{"empty type", strings.Replace(body, `"type":"A"`, `"type":""`, 1), 200, true})
		} else {
			cases = append(cases, struct {
				name, body string
				code       int
				bad        bool
			}{"empty status", strings.Replace(body, `"status":"active"`, `"status":""`, 1), 200, true})
		}
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				count := 0
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
					count++
					want := "/v1/dns/zones/" + testVPCID
					if record {
						want += "/dns_records/" + testRegionID
					}
					if q.Method != "GET" || q.URL.Path != want || q.URL.RawQuery != "" || q.Header.Get("Authorization") != "Bearer test-key" {
						t.Error("unexpected or unauthenticated DNS request")
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.code)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer s.Close()
				c, err := tatnet.NewClientWithResponses(s.URL+"/v1", tatnet.WithAPIKey("test-key"))
				if err != nil {
					t.Fatal(err)
				}
				r := dnsRead(t, &dnsDataSource{client: c, record: record}, id, testVPCID)
				if count != 1 || r.Diagnostics.HasError() != tc.bad {
					t.Fatalf("requests=%d diagnostics=%v", count, r.Diagnostics)
				}
				if tc.bad {
					for _, e := range r.Diagnostics.Errors() {
						if strings.Contains(e.Detail(), "secret marker") {
							t.Fatal("API response leaked")
						}
					}
					return
				}
				if record {
					var m dnsRecordModel
					if ds := r.State.Get(context.Background(), &m); ds.HasError() {
						t.Fatal(ds)
					}
					if m.ID.ValueString() != id || m.ZoneID.ValueString() != testVPCID || m.Name.ValueString() != "pilot.example.com." || m.Content.ValueString() != "192.0.2.10" || m.Type.ValueString() != "A" {
						t.Fatalf("incorrect record %+v", m)
					}
					if tc.name == "optional omitted" && (!m.TTL.IsNull() || !m.Managed.IsNull()) {
						t.Fatal("omitted fields must stay null")
					}
				} else {
					var m dnsZoneModel
					if ds := r.State.Get(context.Background(), &m); ds.HasError() {
						t.Fatal(ds)
					}
					if m.ID.ValueString() != id || m.Name.ValueString() != "example.com." || m.Status.ValueString() != "active" {
						t.Fatalf("incorrect zone %+v", m)
					}
					if tc.name == "optional omitted" && (!m.DefaultTTL.IsNull() || !m.DNSSECEnabled.IsNull()) {
						t.Fatal("omitted fields must stay null")
					}
				}
			})
		}
	}
}

func TestDNSInvalidSelectors(t *testing.T) {
	for _, record := range []bool{false, true} {
		for _, id := range []interface{}{nil, tftypes.UnknownValue, "", "../dns_records", "bad-id"} {
			if r := dnsRead(t, &dnsDataSource{record: record}, id, testVPCID); !r.Diagnostics.HasError() {
				t.Fatal("invalid selector accepted")
			}
			if record {
				if r := dnsRead(t, &dnsDataSource{record: true}, testRegionID, id); !r.Diagnostics.HasError() {
					t.Fatal("invalid zone accepted")
				}
			}
		}
		if r := dnsRead(t, &dnsDataSource{record: record}, testRegionID, testVPCID); !r.Diagnostics.HasError() {
			t.Fatal("missing client accepted")
		}
	}
}

func TestDNSConfigure(t *testing.T) {
	for _, newSource := range []func() datasource.DataSource{NewDNSZoneDataSource, NewDNSRecordDataSource} {
		d := newSource().(*dnsDataSource)
		var empty datasource.ConfigureResponse
		d.Configure(context.Background(), datasource.ConfigureRequest{}, &empty)
		if empty.Diagnostics.HasError() || d.client != nil {
			t.Fatal("nil provider data must defer configuration")
		}
		var bad datasource.ConfigureResponse
		d.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "wrong"}, &bad)
		if !bad.Diagnostics.HasError() {
			t.Fatal("wrong provider data accepted")
		}
		c, err := tatnet.NewClientWithResponses("https://example.com/v1")
		if err != nil {
			t.Fatal(err)
		}
		var good datasource.ConfigureResponse
		d.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: c}, &good)
		if good.Diagnostics.HasError() || d.client != c {
			t.Fatal("provider client not configured")
		}
	}
}
