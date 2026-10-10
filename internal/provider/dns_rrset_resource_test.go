package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

var rrsetRev = strings.Repeat("a", 64)

func testRRsetBody(overrides map[string]any) string {
	m := map[string]any{"id": testRegionID, "zone_id": testVPCID, "name": "test.example.com.", "type": "TXT", "ttl": 300, "records": []string{`"one"`, `"two"`}, "managed": false, "revision": rrsetRev}
	for k, v := range overrides {
		if v == "omit" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}
func testRRsetValues(values ...string) types.Set {
	v := make([]attr.Value, len(values))
	for i, s := range values {
		v[i] = types.StringValue(s)
	}
	return types.SetValueMust(types.StringType, v)
}
func rrsetHarness(t *testing.T, handler http.HandlerFunc) (*dnsRRsetResource, tfsdk.Plan, tfsdk.State) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		want := "/v1/dns/zones/" + testVPCID + "/rrsets"
		if q.Method != "POST" {
			want += "/" + testRegionID
		}
		if q.URL.Path != want || q.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("incorrect scope/auth")
		}
		query := ""
		if q.Method == "DELETE" {
			query = "expected_revision=" + rrsetRev
		}
		if q.URL.RawQuery != query {
			t.Error("revision query lost or rebased")
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, q)
	}))
	t.Cleanup(s.Close)
	c, e := tatnet.NewClientWithResponses(s.URL+"/v1", tatnet.WithAPIKey("test-key"))
	if e != nil {
		t.Fatal(e)
	}
	r := &dnsRRsetResource{client: c}
	ctx := context.Background()
	var schema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schema)
	m := dnsRRsetModel{ID: types.StringUnknown(), ZoneID: types.StringValue(testVPCID), Name: types.StringValue("test.example.com."), Type: types.StringValue("TXT"), TTL: types.Int64Value(300), Records: testRRsetValues(`"one"`, `"two"`), Managed: types.BoolUnknown(), Revision: types.StringUnknown()}
	p := tfsdk.Plan{Schema: schema.Schema}
	if d := p.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	m.ID = types.StringValue(testRegionID)
	m.Managed = types.BoolValue(false)
	m.Revision = types.StringValue(rrsetRev)
	state := tfsdk.State{Schema: schema.Schema}
	if d := state.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	return r, p, state
}
func rrsetState(t *testing.T, s tfsdk.State) dnsRRsetModel {
	t.Helper()
	var m dnsRRsetModel
	if d := s.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return m
}
func TestRRsetCreate(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		code        int
		bad, retain bool
	}{
		{"created", testRRsetBody(nil), 201, false, true}, {"inherited", testRRsetBody(map[string]any{"ttl": nil}), 201, false, true},
		{"wrong parent", testRRsetBody(map[string]any{"zone_id": testNATID}), 201, true, false},
		{"managed", testRRsetBody(map[string]any{"managed": true}), 201, true, true},
		{"missing ownership", testRRsetBody(map[string]any{"managed": "omit"}), 201, true, true},
		{"missing ttl", testRRsetBody(map[string]any{"ttl": "omit"}), 201, true, true},
		{"bad revision", testRRsetBody(map[string]any{"revision": "bad"}), 201, true, true},
		{"different values", testRRsetBody(map[string]any{"records": []string{`"other"`}}), 201, true, true},
		{"duplicate values", testRRsetBody(map[string]any{"records": []string{`"one"`, `"one"`}}), 201, true, true},
		{"bad JSON", "{", 201, true, false}, {"conflict", `{"detail":"secret marker"}`, 409, true, false},
		{"forbidden", `{"detail":"secret marker"}`, 403, true, false}, {"server error", `{"detail":"secret marker"}`, 500, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			r, p, s := rrsetHarness(t, func(w http.ResponseWriter, q *http.Request) {
				calls.Add(1)
				if q.Method != "POST" {
					t.Error("create adopted an existing set")
				}
				var b map[string]any
				_ = json.NewDecoder(q.Body).Decode(&b)
				ttl, ok := b["ttl"]
				want := any(float64(300))
				if tc.name == "inherited" {
					want = nil
				}
				if !ok || ttl != want || len(b) != 4 {
					t.Errorf("unsafe create request %v", b)
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			})
			if tc.name == "inherited" {
				m := rrsetState(t, s)
				m.ID = types.StringUnknown()
				m.TTL = types.Int64Null()
				_ = p.Set(context.Background(), &m)
			}
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
			r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
			if resp.Diagnostics.HasError() != tc.bad || resp.State.Raw.IsNull() == tc.retain || calls.Load() != 1 {
				t.Fatal(resp.Diagnostics, "wrong retention or retries")
			}
			if tc.retain && rrsetState(t, resp.State).ID.ValueString() != testRegionID {
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
func TestRRsetReadsAndConditionalWrites(t *testing.T) {
	for _, op := range []string{"read", "update", "delete"} {
		for _, tc := range []struct {
			name, body         string
			getCode, writeCode int
			bad, write         bool
		}{
			{"success", testRRsetBody(nil), 200, 200, false, true},
			{"inherited update", testRRsetBody(nil), 200, 200, false, true},
			{"absent", `{}`, 404, 204, false, false},
			{"preflight conflict", testRRsetBody(map[string]any{"revision": strings.Repeat("b", 64)}), 200, 200, true, false},
			{"managed", testRRsetBody(map[string]any{"managed": true}), 200, 200, true, false},
			{"missing ownership", testRRsetBody(map[string]any{"managed": "omit"}), 200, 200, true, false},
			{"wrong identity", testRRsetBody(map[string]any{"id": testNATID}), 200, 200, true, false},
			{"wrong parent", testRRsetBody(map[string]any{"zone_id": testNATID}), 200, 200, true, false},
			{"bad records", testRRsetBody(map[string]any{"records": []string{}}), 200, 200, true, false},
			{"negative ttl", testRRsetBody(map[string]any{"ttl": -1}), 200, 200, true, false},
			{"malformed", "{", 200, 200, true, false},
			{"forbidden", `{"detail":"secret marker"}`, 403, 200, true, false},
			{"server error", `{"detail":"secret marker"}`, 500, 200, true, false},
			{"race conflict", testRRsetBody(nil), 200, 409, true, true},
			{"write error", testRRsetBody(nil), 200, 500, true, true},
		} {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				var reads, writes atomic.Int32
				r, p, s := rrsetHarness(t, func(w http.ResponseWriter, q *http.Request) {
					if q.Method == "GET" {
						reads.Add(1)
						w.WriteHeader(tc.getCode)
						_, _ = w.Write([]byte(tc.body))
						return
					}
					writes.Add(1)
					if op == "update" {
						if q.Method != "PUT" {
							t.Error("wrong method")
						}
						var b map[string]any
						_ = json.NewDecoder(q.Body).Decode(&b)
						ttl, ok := b["ttl"]
						want := any(float64(600))
						if tc.name == "inherited update" {
							want = nil
						}
						if !ok || ttl != want || b["expected_revision"] != rrsetRev || len(b) != 3 {
							t.Errorf("unsafe/rebased replace %v", b)
						}
						w.WriteHeader(tc.writeCode)
						body := testRRsetBody(map[string]any{"ttl": 600, "records": []string{`"next"`}, "revision": strings.Repeat("b", 64)})
						if tc.name == "inherited update" {
							body = testRRsetBody(map[string]any{"ttl": nil, "records": []string{`"next"`}, "revision": strings.Repeat("b", 64)})
						}
						_, _ = w.Write([]byte(body))
					} else {
						if q.Method != "DELETE" {
							t.Error("wrong delete method")
						}
						code := 204
						if tc.writeCode != 200 {
							code = tc.writeCode
						}
						w.WriteHeader(code)
					}
				})
				ctx := context.Background()
				wantBad, wantWrite := tc.bad, tc.write
				result := s
				var bad bool
				switch op {
				case "read":
					resp := resource.ReadResponse{State: s}
					r.Read(ctx, resource.ReadRequest{State: s}, &resp)
					result = resp.State
					bad = resp.Diagnostics.HasError()
					wantWrite = false
					if tc.name == "preflight conflict" || tc.name == "managed" || tc.name == "race conflict" || tc.name == "write error" {
						wantBad = false
					}
				case "update":
					m := rrsetState(t, s)
					m.Records = testRRsetValues(`"next"`)
					m.TTL = types.Int64Value(600)
					if tc.name == "inherited update" {
						m.TTL = types.Int64Null()
					}
					_ = p.Set(ctx, &m)
					resp := resource.UpdateResponse{State: s}
					r.Update(ctx, resource.UpdateRequest{State: s, Plan: p}, &resp)
					result = resp.State
					bad = resp.Diagnostics.HasError()
					if tc.name == "absent" {
						wantBad = true
					}
					if !bad && !rrsetState(t, result).Records.Equal(m.Records) {
						t.Fatal("values not updated")
					}
				case "delete":
					resp := resource.DeleteResponse{State: s}
					r.Delete(ctx, resource.DeleteRequest{State: s}, &resp)
					bad = resp.Diagnostics.HasError()
					result = resp.State
				}
				if bad != wantBad || (writes.Load() > 0) != wantWrite || reads.Load() != 1 {
					t.Fatalf("bad=%v want=%v reads=%d writes=%d", bad, wantBad, reads.Load(), writes.Load())
				}
				if wantBad && !result.Raw.Equal(s.Raw) {
					t.Fatal("failed mutation lost state")
				}
				if op == "read" && tc.name == "absent" && !result.Raw.IsNull() {
					t.Fatal("404 not removed")
				}
			})
		}
	}
}
func TestRRsetImportValidationAndReplacement(t *testing.T) {
	ctx := context.Background()
	r, p, s := rrsetHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("local operation used API") })
	for _, id := range []string{testVPCID + "/" + testRegionID, "bad", testVPCID + "/bad", "", testVPCID + "/" + testRegionID + "/extra"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		valid := id == testVPCID+"/"+testRegionID
		if resp.Diagnostics.HasError() == valid {
			t.Fatal(resp.Diagnostics)
		}
		if valid {
			m := rrsetState(t, resp.State)
			if m.ID.ValueString() != testRegionID || m.ZoneID.ValueString() != testVPCID {
				t.Fatal("import identity lost")
			}
		}
	}
	for _, field := range []string{"zone_id", "name", "type", "ttl", "records"} {
		m := rrsetState(t, s)
		switch field {
		case "zone_id":
			m.ZoneID = types.StringValue(testNATID)
		case "name":
			m.Name = types.StringValue("other.example.com.")
		case "type":
			m.Type = types.StringValue("CNAME")
			m.Records = testRRsetValues("target.example.com.")
		case "ttl":
			m.TTL = types.Int64Value(600)
		case "records":
			m.Records = testRRsetValues(`"changed"`)
		}
		_ = p.Set(ctx, &m)
		prior, _ := tfprotov6.NewDynamicValue(s.Raw.Type(), s.Raw)
		proposed, _ := tfprotov6.NewDynamicValue(p.Raw.Type(), p.Raw)
		server := providerserver.NewProtocol6(New())()
		resp, e := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: "tatnet_dns_rrset", PriorState: &prior, ProposedNewState: &proposed, Config: &proposed})
		if e != nil {
			t.Fatal(e)
		}
		for _, d := range resp.Diagnostics {
			if d.Severity == tfprotov6.DiagnosticSeverityError {
				t.Fatal(d.Detail)
			}
		}
		if (len(resp.RequiresReplace) > 0) != (field == "zone_id" || field == "name" || field == "type") {
			t.Fatal("wrong replacement semantics", field)
		}
	}
	for _, kind := range []string{"negative ttl", "overflow ttl", "empty records", "unknown records", "bad value", "missing client", "invalid zone"} {
		m := rrsetState(t, s)
		target := *r
		valid := false
		switch kind {
		case "negative ttl":
			m.TTL = types.Int64Value(-1)
		case "overflow ttl":
			m.TTL = types.Int64Value(2147483648)
		case "empty records":
			m.Records = testRRsetValues()
		case "unknown records":
			m.Records = types.SetUnknown(types.StringType)
			valid = true
		case "bad value":
			m.Type = types.StringValue("A")
			m.Records = testRRsetValues("not-ip")
		case "missing client":
			target.client = nil
			valid = true
		case "invalid zone":
			m.ZoneID = types.StringValue("../zone")
		}
		_ = p.Set(ctx, &m)
		resp := resource.ValidateConfigResponse{}
		target.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: p.Schema, Raw: p.Raw}}, &resp)
		if resp.Diagnostics.HasError() == valid {
			t.Fatal(kind, resp.Diagnostics)
		}
		cr := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
		target.Create(ctx, resource.CreateRequest{Plan: p}, &cr)
		if !cr.Diagnostics.HasError() || !cr.State.Raw.IsNull() {
			t.Fatal("invalid create permitted", kind)
		}
	}
	for _, c := range []any{nil, "bad", r.client} {
		target := &dnsRRsetResource{}
		resp := resource.ConfigureResponse{}
		target.Configure(ctx, resource.ConfigureRequest{ProviderData: c}, &resp)
		if resp.Diagnostics.HasError() != (c == "bad") {
			t.Fatal(resp.Diagnostics)
		}
	}
}

func TestRRsetUncertainWritesAndResponseGuards(t *testing.T) {
	for _, op := range []string{"create", "update", "delete"} {
		for _, failure := range []string{"transport", "forbidden", "bad response"} {
			t.Run(op+"/"+failure, func(t *testing.T) {
				var writes atomic.Int32
				r, p, s := rrsetHarness(t, func(w http.ResponseWriter, q *http.Request) {
					if q.Method == "GET" {
						_, _ = w.Write([]byte(testRRsetBody(nil)))
						return
					}
					writes.Add(1)
					if failure == "transport" {
						conn, _, e := w.(http.Hijacker).Hijack()
						if e != nil {
							t.Error(e)
							return
						}
						_ = conn.Close()
						return
					}
					if failure == "forbidden" {
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"detail":"secret marker"}`))
						return
					}
					if op == "delete" {
						w.WriteHeader(500)
						return
					}
					code := 200
					if op == "create" {
						code = 201
					}
					w.WriteHeader(code)
					_, _ = w.Write([]byte(testRRsetBody(map[string]any{"zone_id": testNATID})))
				})
				ctx := context.Background()
				var bad bool
				var result tfsdk.State
				switch op {
				case "create":
					resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
					r.Create(ctx, resource.CreateRequest{Plan: p}, &resp)
					bad = resp.Diagnostics.HasError()
					result = resp.State
					if !result.Raw.IsNull() {
						t.Fatal("invented or foreign ID retained")
					}
				case "update":
					resp := resource.UpdateResponse{State: s}
					r.Update(ctx, resource.UpdateRequest{State: s, Plan: p}, &resp)
					bad = resp.Diagnostics.HasError()
					result = resp.State
				case "delete":
					resp := resource.DeleteResponse{State: s}
					r.Delete(ctx, resource.DeleteRequest{State: s}, &resp)
					bad = resp.Diagnostics.HasError()
					result = resp.State
				}
				if !bad || writes.Load() != 1 {
					t.Fatal("unsafe retry or ignored failure")
				}
				if op != "create" && !result.Raw.Equal(s.Raw) {
					t.Fatal("uncertain write lost state")
				}
			})
		}
	}
}
