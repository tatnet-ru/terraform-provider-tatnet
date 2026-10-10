package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

func dnsResourceHarness(t *testing.T, handler http.HandlerFunc) (*dnsRecordResource, tfsdk.Plan, tfsdk.State) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get("Authorization") != "Bearer test-key" || q.URL.RawQuery != "" {
			t.Error("incorrect auth or query")
		}
		want := "/v1/dns/zones/" + testVPCID + "/dns_records"
		if q.Method != "POST" {
			want += "/" + testRegionID
		}
		if q.URL.Path != want {
			t.Error("incorrect scoped path")
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, q)
	}))
	t.Cleanup(s.Close)
	c, e := tatnet.NewClientWithResponses(s.URL+"/v1", tatnet.WithAPIKey("test-key"))
	if e != nil {
		t.Fatal(e)
	}
	r := &dnsRecordResource{client: c}
	ctx := context.Background()
	var schema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schema)
	m := dnsRecordModel{ID: types.StringUnknown(), ZoneID: types.StringValue(testVPCID), Name: types.StringValue("pilot.example.com."), Type: types.StringValue("A"), Content: types.StringValue("192.0.2.10"), TTL: types.Int64Unknown(), Managed: types.BoolUnknown()}
	plan := tfsdk.Plan{Schema: schema.Schema}
	if d := plan.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	m.ID = types.StringValue(testRegionID)
	m.TTL = types.Int64Value(300)
	m.Managed = types.BoolValue(false)
	state := tfsdk.State{Schema: schema.Schema}
	if d := state.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	return r, plan, state
}
func dnsResourceState(t *testing.T, s tfsdk.State) dnsRecordModel {
	t.Helper()
	var m dnsRecordModel
	if d := s.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return m
}
func TestDNSRecordCreate(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		code        int
		bad, retain bool
	}{
		{"created", dnsRecordBody, 201, false, true},
		{"inherited TTL", strings.Replace(dnsRecordBody, `"ttl":300`, `"ttl":null`, 1), 201, false, true},
		{"different content", strings.Replace(dnsRecordBody, "192.0.2.10", "192.0.2.20", 1), 201, true, true},
		{"managed", strings.Replace(dnsRecordBody, `"managed":false`, `"managed":true`, 1), 201, true, true},
		{"ownership missing", strings.Replace(dnsRecordBody, `,"managed":false`, "", 1), 201, true, true},
		{"wrong parent", strings.Replace(dnsRecordBody, testVPCID, testNATID, 1), 201, true, false},
		{"wrong name", strings.Replace(dnsRecordBody, "pilot.example.com.", "other.example.com.", 1), 201, true, true},
		{"negative ttl", strings.Replace(dnsRecordBody, ":300", ":-1", 1), 201, true, true},
		{"malformed", "{", 201, true, false}, {"empty", "{}", 201, true, false},
		{"conflict", `{"detail":"secret marker"}`, 409, true, false}, {"forbidden", `{"detail":"secret marker"}`, 403, true, false}, {"server error", `{"detail":"secret marker"}`, 500, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r, p, s := dnsResourceHarness(t, func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != "POST" {
					t.Error("non-create request")
				}
				var b map[string]any
				if e := json.NewDecoder(q.Body).Decode(&b); e != nil {
					t.Fatal(e)
				}
				if len(b) != 3 || b["name"] != "pilot.example.com." || b["type"] != "A" || b["content"] != "192.0.2.10" {
					t.Fatalf("unsafe create body: %v", b)
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			})
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
			r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
			if resp.Diagnostics.HasError() != tc.bad || calls != 1 {
				t.Fatal(resp.Diagnostics, calls)
			}
			if resp.State.Raw.IsNull() == tc.retain {
				t.Fatal("wrong identity retention")
			}
			if tc.retain && dnsResourceState(t, resp.State).ID.ValueString() != testRegionID {
				t.Fatal("created ID lost")
			}
			for _, d := range resp.Diagnostics {
				if strings.Contains(d.Detail(), "secret marker") {
					t.Fatal("body leaked")
				}
			}
		})
	}
}
func TestDNSRecordReadUpdateDelete(t *testing.T) {
	for _, op := range []string{"read", "update", "delete"} {
		for _, tc := range []struct {
			name, body         string
			getCode, writeCode int
			bad, write, absent bool
		}{
			{"success", dnsRecordBody, 200, 200, false, true, false},
			{"absent", `{}`, 404, 204, false, false, true},
			{"managed", strings.Replace(dnsRecordBody, `"managed":false`, `"managed":true`, 1), 200, 200, true, false, false},
			{"ownership missing", strings.Replace(dnsRecordBody, `,"managed":false`, "", 1), 200, 200, true, false, false},
			{"moved record", strings.Replace(dnsRecordBody, "pilot.example.com.", "other.example.com.", 1), 200, 200, true, false, false},
			{"wrong ID", strings.Replace(dnsRecordBody, testRegionID, testNATID, 1), 200, 200, true, false, false},
			{"wrong zone", strings.Replace(dnsRecordBody, testVPCID, testNATID, 1), 200, 200, true, false, false},
			{"malformed", "{", 200, 200, true, false, false},
			{"forbidden", `{"detail":"secret marker"}`, 403, 200, true, false, false},
			{"server error", `{"detail":"secret marker"}`, 500, 200, true, false, false},
			{"write failure", dnsRecordBody, 200, 500, true, true, false},
		} {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				reads, writes := 0, 0
				r, p, s := dnsResourceHarness(t, func(w http.ResponseWriter, q *http.Request) {
					if q.Method == "GET" {
						reads++
						w.WriteHeader(tc.getCode)
						_, _ = w.Write([]byte(tc.body))
						return
					}
					writes++
					if op == "update" {
						if q.Method != "PATCH" {
							t.Error("wrong update method")
						}
						var b map[string]any
						_ = json.NewDecoder(q.Body).Decode(&b)
						if len(b) != 1 || b["content"] != "192.0.2.20" {
							t.Errorf("unsafe update body %v", b)
						}
						w.WriteHeader(tc.writeCode)
						_, _ = w.Write([]byte(strings.Replace(dnsRecordBody, "192.0.2.10", "192.0.2.20", 1)))
					} else {
						if q.Method != "DELETE" {
							t.Error("wrong delete method")
						}
						code := 204
						if tc.writeCode == 500 {
							code = 500
						}
						w.WriteHeader(code)
					}
				})
				ctx := context.Background()
				wantBad := tc.bad
				wantWrite := tc.write
				var hasError bool
				result := s
				switch op {
				case "read":
					resp := resource.ReadResponse{State: s}
					r.Read(ctx, resource.ReadRequest{State: s}, &resp)
					hasError = resp.Diagnostics.HasError()
					result = resp.State
					wantWrite = false
					if tc.name == "managed" || tc.name == "ownership missing" || tc.name == "moved record" || tc.name == "write failure" {
						wantBad = false
					}
				case "update":
					m := dnsResourceState(t, s)
					m.Content = types.StringValue("192.0.2.20")
					m.TTL = types.Int64Unknown()
					m.Managed = types.BoolUnknown()
					_ = p.Set(ctx, &m)
					resp := resource.UpdateResponse{State: s}
					r.Update(ctx, resource.UpdateRequest{Plan: p, State: s}, &resp)
					hasError = resp.Diagnostics.HasError()
					result = resp.State
					if tc.absent {
						wantBad = true
					}
					if !hasError && dnsResourceState(t, result).Content.ValueString() != "192.0.2.20" {
						t.Fatal("content not updated")
					}
				case "delete":
					resp := resource.DeleteResponse{State: s}
					r.Delete(ctx, resource.DeleteRequest{State: s}, &resp)
					hasError = resp.Diagnostics.HasError()
					result = resp.State
				}
				if hasError != wantBad || reads != 1 || (writes > 0) != wantWrite {
					t.Fatalf("error=%v want=%v reads=%d writes=%d", hasError, wantBad, reads, writes)
				}
				if wantBad && !result.Raw.Equal(s.Raw) {
					t.Fatal("failed operation lost state")
				}
				if op == "read" && tc.absent && !result.Raw.IsNull() {
					t.Fatal("404 did not remove state")
				}
			})
		}
	}
}
func TestDNSRecordValidationImportAndPlan(t *testing.T) {
	ctx := context.Background()
	r, p, s := dnsResourceHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("local operation called API") })
	for _, tc := range []struct {
		field, value string
		bad          bool
	}{
		{"zone_id", "../bad", true}, {"name", "relative", true}, {"name", "Pilot.example.com.", true}, {"name", "bad..example.com.", true},
		{"type", "NS", true}, {"type", "SOA", true}, {"type", "txt", true}, {"content", " 192.0.2.10", true}, {"content", "not-ip", true},
		{"TXT", "\"test\"", false}, {"AAAA", "2001:db8::1", false}, {"AAAA", "192.0.2.10", true}, {"CNAME", "target.example.com.", false}, {"CNAME", "target.example.com", true},
	} {
		m := dnsResourceState(t, s)
		switch tc.field {
		case "zone_id":
			m.ZoneID = types.StringValue(tc.value)
		case "name":
			m.Name = types.StringValue(tc.value)
		case "type":
			m.Type = types.StringValue(tc.value)
		case "content":
			m.Content = types.StringValue(tc.value)
		default:
			m.Type = types.StringValue(tc.field)
			m.Content = types.StringValue(tc.value)
		}
		_ = p.Set(ctx, &m)
		resp := resource.ValidateConfigResponse{}
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: p.Schema, Raw: p.Raw}}, &resp)
		if resp.Diagnostics.HasError() != tc.bad {
			t.Fatal(tc, resp.Diagnostics)
		}
	}
	for _, id := range []string{testVPCID + "/" + testRegionID, testVPCID, "bad/" + testRegionID, "", testVPCID + "/" + testRegionID + "/extra"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		valid := id == testVPCID+"/"+testRegionID
		if resp.Diagnostics.HasError() == valid {
			t.Fatal(resp.Diagnostics)
		}
		if valid {
			m := dnsResourceState(t, resp.State)
			if m.ID.ValueString() != testRegionID || m.ZoneID.ValueString() != testVPCID {
				t.Fatal("import selector lost")
			}
		}
	}
	for _, field := range []string{"zone_id", "name", "type", "content"} {
		m := dnsResourceState(t, s)
		switch field {
		case "zone_id":
			m.ZoneID = types.StringValue(testNATID)
		case "name":
			m.Name = types.StringValue("other.example.com.")
		case "type":
			m.Type = types.StringValue("TXT")
		case "content":
			m.Content = types.StringValue("192.0.2.20")
		}
		_ = p.Set(ctx, &m)
		prior, _ := tfprotov6.NewDynamicValue(s.Raw.Type(), s.Raw)
		proposed, _ := tfprotov6.NewDynamicValue(p.Raw.Type(), p.Raw)
		server := providerserver.NewProtocol6(New())()
		resp, e := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: "tatnet_dns_record", PriorState: &prior, ProposedNewState: &proposed, Config: &proposed})
		if e != nil {
			t.Fatal(e)
		}
		for _, d := range resp.Diagnostics {
			if d.Severity == tfprotov6.DiagnosticSeverityError {
				t.Fatal(d.Detail)
			}
		}
		if (len(resp.RequiresReplace) > 0) != (field != "content") {
			t.Fatal("wrong replacement semantics", field)
		}
	}
	for _, c := range []any{nil, "bad", r.client} {
		target := &dnsRecordResource{}
		resp := resource.ConfigureResponse{}
		target.Configure(ctx, resource.ConfigureRequest{ProviderData: c}, &resp)
		if resp.Diagnostics.HasError() != (c == "bad") {
			t.Fatal(resp.Diagnostics)
		}
	}
}

func TestDNSRecordLocalGuards(t *testing.T) {
	ctx := context.Background()
	r, p, s := dnsResourceHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("guard called API") })
	for _, kind := range []string{"missing client", "bad ID", "bad zone", "unknown content"} {
		t.Run(kind, func(t *testing.T) {
			target := *r
			m := dnsResourceState(t, s)
			switch kind {
			case "missing client":
				target.client = nil
			case "bad ID":
				m.ID = types.StringValue("../bad")
			case "bad zone":
				m.ZoneID = types.StringValue("../bad")
			case "unknown content":
				m.Content = types.StringUnknown()
			}
			state := s
			_ = state.Set(ctx, &m)
			plan := p
			_ = plan.Set(ctx, &m)
			if kind != "unknown content" {
				rd := resource.ReadResponse{State: state}
				target.Read(ctx, resource.ReadRequest{State: state}, &rd)
				del := resource.DeleteResponse{State: state}
				target.Delete(ctx, resource.DeleteRequest{State: state}, &del)
				if !rd.Diagnostics.HasError() || !del.Diagnostics.HasError() || !rd.State.Raw.Equal(state.Raw) {
					t.Fatal("guard lost state")
				}
			}
			if kind != "bad ID" {
				cr := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
				target.Create(ctx, resource.CreateRequest{Plan: plan}, &cr)
				if !cr.Diagnostics.HasError() || !cr.State.Raw.IsNull() {
					t.Fatal("invalid create allowed")
				}
			}
		})
	}
}
func TestDNSRecordTransportFailure(t *testing.T) {
	for _, op := range []string{"create", "read", "update", "delete"} {
		t.Run(op, func(t *testing.T) {
			var calls atomic.Int32
			r, p, s := dnsResourceHarness(t, func(w http.ResponseWriter, q *http.Request) {
				calls.Add(1)
				conn, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Fatal(e)
				}
				_ = conn.Close()
			})
			ctx := context.Background()
			var bad bool
			switch op {
			case "create":
				resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
				r.Create(ctx, resource.CreateRequest{Plan: p}, &resp)
				bad = resp.Diagnostics.HasError()
				if !resp.State.Raw.IsNull() {
					t.Fatal("invented ID")
				}
			case "read":
				resp := resource.ReadResponse{State: s}
				r.Read(ctx, resource.ReadRequest{State: s}, &resp)
				bad = resp.Diagnostics.HasError()
				if !resp.State.Raw.Equal(s.Raw) {
					t.Fatal("state lost")
				}
			case "update":
				resp := resource.UpdateResponse{State: s}
				r.Update(ctx, resource.UpdateRequest{Plan: p, State: s}, &resp)
				bad = resp.Diagnostics.HasError()
				if !resp.State.Raw.Equal(s.Raw) {
					t.Fatal("state lost")
				}
			case "delete":
				resp := resource.DeleteResponse{State: s}
				r.Delete(ctx, resource.DeleteRequest{State: s}, &resp)
				bad = resp.Diagnostics.HasError()
			}
			if !bad || calls.Load() != 1 {
				t.Fatal("transport failure retried or ignored", calls.Load())
			}
		})
	}
}

func TestDNSRecordWriteResponses(t *testing.T) {
	for _, op := range []string{"update", "delete"} {
		cases := []struct {
			name, body string
			code       int
			bad        bool
		}{
			{"forbidden", `{"detail":"secret marker"}`, 403, true}, {"conflict", `{"detail":"secret marker"}`, 409, true}, {"transport", "", 0, true},
		}
		if op == "update" {
			cases = append(cases, struct {
				name, body string
				code       int
				bad        bool
			}{"wrong parent", strings.Replace(dnsRecordBody, testVPCID, testNATID, 1), 200, true}, struct {
				name, body string
				code       int
				bad        bool
			}{"wrong content", strings.Replace(dnsRecordBody, "192.0.2.10", "192.0.2.30", 1), 200, true}, struct {
				name, body string
				code       int
				bad        bool
			}{"malformed", "{", 200, true})
		} else {
			cases = append(cases, struct {
				name, body string
				code       int
				bad        bool
			}{"already deleted", "", 404, false})
		}
		for _, tc := range cases {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				var writes atomic.Int32
				r, p, s := dnsResourceHarness(t, func(w http.ResponseWriter, q *http.Request) {
					if q.Method == "GET" {
						_, _ = w.Write([]byte(dnsRecordBody))
						return
					}
					writes.Add(1)
					if tc.code == 0 {
						conn, _, e := w.(http.Hijacker).Hijack()
						if e != nil {
							t.Error(e)
							return
						}
						_ = conn.Close()
						return
					}
					w.WriteHeader(tc.code)
					_, _ = w.Write([]byte(tc.body))
				})
				ctx := context.Background()
				var bad bool
				var result tfsdk.State
				if op == "update" {
					m := dnsResourceState(t, s)
					m.Content = types.StringValue("192.0.2.20")
					_ = p.Set(ctx, &m)
					resp := resource.UpdateResponse{State: s}
					r.Update(ctx, resource.UpdateRequest{Plan: p, State: s}, &resp)
					bad = resp.Diagnostics.HasError()
					result = resp.State
					for _, d := range resp.Diagnostics {
						if strings.Contains(d.Detail(), "secret marker") {
							t.Fatal("body leaked")
						}
					}
				} else {
					resp := resource.DeleteResponse{State: s}
					r.Delete(ctx, resource.DeleteRequest{State: s}, &resp)
					bad = resp.Diagnostics.HasError()
					result = resp.State
					for _, d := range resp.Diagnostics {
						if strings.Contains(d.Detail(), "secret marker") {
							t.Fatal("body leaked")
						}
					}
				}
				if bad != tc.bad || writes.Load() != 1 {
					t.Fatal("write error mishandled or retried", writes.Load())
				}
				if tc.bad && !result.Raw.Equal(s.Raw) {
					t.Fatal("state lost")
				}
			})
		}
	}
}
