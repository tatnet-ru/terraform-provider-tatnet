package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

const testNATID = "33333333-3333-4333-8333-333333333333"
const natBody = `{"enabled":true,"fip_id":"33333333-3333-4333-8333-333333333333","address":"192.0.2.42","status":"attached"}`
const releasingIPBody = `{"id":"33333333-3333-4333-8333-333333333333","cluster_id":"22222222-2222-4222-8222-222222222222","address":"192.0.2.42","status":"detaching","target_kind":"interface","auto_release":true}`

func natVPC(body string) string {
	return strings.TrimSuffix(vpcBody, "}") + `,"nat_gateway":` + body + "}"
}
func natHarness(t *testing.T, handler http.HandlerFunc) (*natGatewayResource, tfsdk.Plan, tfsdk.State) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get("Authorization") != "Bearer test-key" || q.URL.RawQuery != "" {
			t.Error("unexpected authentication/query")
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, q)
	}))
	t.Cleanup(s.Close)
	c, err := tatnet.NewClientWithResponses(s.URL+"/v1", tatnet.WithAPIKey("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	r := &natGatewayResource{client: c, pollInterval: time.Millisecond, operationTimeout: 100 * time.Millisecond}
	ctx := context.Background()
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	m := natGatewayModel{ID: types.StringUnknown(), VPCID: types.StringValue(testVPCID), ClusterID: types.StringUnknown(), Address: types.StringUnknown(), Status: types.StringUnknown(), Enabled: types.BoolUnknown()}
	plan := tfsdk.Plan{Schema: sr.Schema}
	if d := plan.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	m.ID = types.StringValue(testNATID)
	m.ClusterID = types.StringValue(testRegionID)
	m.Address = types.StringValue("192.0.2.42")
	m.Status = types.StringValue("attached")
	m.Enabled = types.BoolValue(true)
	state := tfsdk.State{Schema: sr.Schema}
	if d := state.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	return r, plan, state
}
func natState(t *testing.T, s tfsdk.State) natGatewayModel {
	t.Helper()
	var m natGatewayModel
	if d := s.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return m
}
func TestNATCreate(t *testing.T) {
	for _, tc := range []struct {
		name, postBody, getBody string
		postCode                int
		err, retain             bool
	}{
		{"attached", natBody, natVPC(natBody), 201, false, true},
		{"poll until attached", natBody, natVPC(natBody), 201, false, true},
		{"error", natBody, natVPC(strings.Replace(natBody, `"attached"`, `"error"`, 1)), 201, true, true},
		{"timeout", natBody, natVPC(strings.Replace(natBody, `"attached"`, `"attaching"`, 1)), 201, true, true},
		{"disabled", natBody, natVPC(strings.Replace(natBody, `true`, `false`, 1)), 201, true, true},
		{"foreign allocation", natBody, natVPC(strings.Replace(natBody, testNATID, testRegionID, 1)), 201, true, true},
		{"unknown status", natBody, natVPC(strings.Replace(natBody, `"attached"`, `"unexpected"`, 1)), 201, true, true},
		{"missing gateway", natBody, vpcBody, 201, true, true},
		{"invalid IP", strings.Replace(natBody, "192.0.2.42", "bad", 1), natVPC(natBody), 201, true, true},
		{"malformed followup", natBody, `{`, 201, true, true},
		{"region mismatch", natBody, strings.Replace(natVPC(natBody), testRegionID, testVPCID, 1), 201, true, true},
		{"empty creation", `{}`, vpcBody, 201, true, false},
		{"bad JSON", `{`, vpcBody, 201, true, false},
		{"insufficient funds", `{"detail":"secret marker"}`, vpcBody, 402, true, false},
		{"billing unavailable", `{"detail":"secret marker"}`, vpcBody, 503, true, false},
		{"conflict", `{"detail":"secret marker"}`, vpcBody, 409, true, false},
		{"forbidden", `{"detail":"secret marker"}`, vpcBody, 403, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts, reads atomic.Int32
			r, plan, state := natHarness(t, func(w http.ResponseWriter, q *http.Request) {
				switch {
				case q.Method == "POST" && q.URL.Path == "/v1/vpcs/"+testVPCID+"/nat-gateway":
					posts.Add(1)
					var input tatnet.V1NatGatewayEnable
					if err := json.NewDecoder(q.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					if input.FipId != nil {
						t.Error("reused an external IP")
					}
					w.WriteHeader(tc.postCode)
					_, _ = w.Write([]byte(tc.postBody))
				case q.Method == "GET" && q.URL.Path == "/v1/vpcs/"+testVPCID:
					n := reads.Add(1)
					body := tc.getBody
					if posts.Load() == 0 {
						body = vpcBody
					} else if tc.name == "poll until attached" && n == 2 {
						body = natVPC(strings.Replace(natBody, `"attached"`, `"attaching"`, 1))
					}
					_, _ = w.Write([]byte(body))
				default:
					t.Errorf("unexpected request %s %s", q.Method, q.URL.Path)
				}
			})
			state.Raw = tftypes.NewValue(state.Raw.Type(), nil)
			resp := resource.CreateResponse{State: state}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
			if resp.Diagnostics.HasError() != tc.err {
				t.Fatalf("diagnostics %v", resp.Diagnostics)
			}
			if posts.Load() != 1 {
				t.Fatal("POST retried or omitted")
			}
			if tc.retain {
				if natState(t, resp.State).ID.ValueString() != testNATID {
					t.Fatal("allocation ID lost")
				}
			} else if !resp.State.Raw.IsNull() {
				t.Fatal("claimed an unknown allocation")
			}
			if !tc.err && natState(t, resp.State).Status.ValueString() != "attached" {
				t.Fatal("did not wait")
			}
			if tc.err && strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "secret marker") {
				t.Fatal("API error body leaked")
			}
		})
	}
}
func TestNATCreatePreflight(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
	}{
		{"existing gateway", natVPC(natBody), 200}, {"detaching gateway", natVPC(strings.Replace(natBody, `"attached"`, `"detaching"`, 1)), 200},
		{"pending VPC", strings.Replace(vpcBody, `"active"`, `"pending"`, 1), 200}, {"missing VPC", `{}`, 404}, {"forbidden VPC", `{"detail":"secret marker"}`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, plan, state := natHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" {
					t.Error("preflight wrote to API")
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			})
			state.Raw = tftypes.NewValue(state.Raw.Type(), nil)
			resp := resource.CreateResponse{State: state}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
			if !resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
				t.Fatal("adopted existing/unknown gateway")
			}
		})
	}
}
func TestNATDelete(t *testing.T) {
	for _, name := range []string{"normal", "delayed release", "already detaching", "absent", "missing VPC", "manual retention", "foreign gateway", "disable forbidden", "disable server error", "disable raced 404", "timeout", "IP forbidden", "IP mismatch", "IP retargeted", "network error", "malformed disable", "inconsistent missing IP", "VPC forbidden", "poll forbidden", "poll switched gateway", "IP malformed", "invalid gateway address"} {
		t.Run(name, func(t *testing.T) {
			var deletes, reads, ipReads atomic.Int32
			r, _, state := natHarness(t, func(w http.ResponseWriter, q *http.Request) {
				switch {
				case q.Method == "GET" && q.URL.Path == "/v1/vpcs/"+testVPCID:
					n := reads.Add(1)
					if name == "VPC forbidden" || (name == "poll forbidden" && deletes.Load() > 0) {
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"detail":"secret marker"}`))
						return
					}
					body := natVPC(natBody)
					if name == "missing VPC" {
						w.WriteHeader(404)
						_, _ = w.Write([]byte(`{}`))
						return
					}
					if name == "absent" || name == "manual retention" {
						body = vpcBody
					}
					if name == "foreign gateway" {
						body = natVPC(strings.Replace(natBody, testNATID, testRegionID, 1))
					}
					if name == "already detaching" || deletes.Load() > 0 {
						body = vpcBody
						if (name == "delayed release" && n < 4) || name == "timeout" || name == "inconsistent missing IP" {
							body = natVPC(strings.Replace(strings.Replace(natBody, `true`, `false`, 1), `"attached"`, `"detaching"`, 1))
						}
					}
					if name == "already detaching" && n == 1 {
						body = natVPC(strings.Replace(strings.Replace(natBody, `true`, `false`, 1), `"attached"`, `"detaching"`, 1))
					}
					if name == "poll switched gateway" && deletes.Load() > 0 {
						body = natVPC(strings.Replace(natBody, testNATID, testRegionID, 1))
					}
					if name == "invalid gateway address" {
						body = natVPC(strings.Replace(natBody, "192.0.2.42", "bad", 1))
					}
					if name == "network error" && deletes.Load() > 0 {
						body = natVPC(strings.Replace(natBody, `"attached"`, `"error"`, 1))
					}
					_, _ = w.Write([]byte(body))
				case q.Method == "DELETE" && q.URL.Path == "/v1/vpcs/"+testVPCID+"/nat-gateway":
					deletes.Add(1)
					code := 202
					body := strings.Replace(strings.Replace(natBody, `true`, `false`, 1), `"attached"`, `"detaching"`, 1)
					switch name {
					case "disable forbidden":
						code = 403
						body = `{"detail":"secret marker"}`
					case "disable server error":
						code = 500
						body = `{"detail":"secret marker"}`
					case "disable raced 404":
						code = 404
						body = `{}`
					case "malformed disable":
						body = `{`
					}
					w.WriteHeader(code)
					_, _ = w.Write([]byte(body))
				case q.Method == "GET" && q.URL.Path == "/v1/floating-ips/"+testNATID:
					n := ipReads.Add(1)
					code := 404
					body := `{}`
					switch name {
					case "delayed release":
						if n < 3 {
							code = 200
							body = releasingIPBody
						}
					case "timeout":
						code = 200
						body = releasingIPBody
					case "manual retention":
						code = 200
						body = strings.Replace(releasingIPBody, `true`, `false`, 1)
					case "IP malformed":
						code = 200
						body = `{`
					case "IP forbidden":
						code = 403
						body = `{"detail":"secret marker"}`
					case "IP mismatch":
						code = 200
						body = strings.Replace(releasingIPBody, testNATID, testRegionID, 1)
					case "IP retargeted":
						code = 200
						body = strings.TrimSuffix(releasingIPBody, "}") + `,"vm_interface_id":"` + testVPCID + `"}`
					}
					w.WriteHeader(code)
					_, _ = w.Write([]byte(body))
				default:
					t.Errorf("unexpected request %s %s", q.Method, q.URL.Path)
				}
			})
			resp := resource.DeleteResponse{State: state}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
			success := name == "normal" || name == "delayed release" || name == "already detaching" || name == "absent" || name == "missing VPC" || name == "disable raced 404"
			if resp.Diagnostics.HasError() == success {
				t.Fatalf("diagnostics %v", resp.Diagnostics)
			}
			if success {
				if !resp.State.Raw.IsNull() {
					t.Fatal("released allocation retained")
				}
			} else {
				if !resp.State.Raw.Equal(state.Raw) {
					t.Fatal("failed disable lost state")
				}
				if strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "secret marker") {
					t.Fatal("error body leaked")
				}
			}
			noDelete := name == "already detaching" || name == "absent" || name == "missing VPC" || name == "manual retention" || name == "foreign gateway" || name == "VPC forbidden" || name == "invalid gateway address"
			if (deletes.Load() == 0) != noDelete || deletes.Load() > 1 {
				t.Fatal("unsafe or repeated disable", deletes.Load())
			}
			if name == "delayed release" && ipReads.Load() < 3 {
				t.Fatal("did not wait for IP release")
			}
		})
	}
}
func TestNATRead(t *testing.T) {
	for _, name := range []string{"attached", "error", "foreign gateway", "pending release", "fully gone", "missing VPC paid IP", "IP retained manually", "read forbidden", "malformed read"} {
		t.Run(name, func(t *testing.T) {
			r, _, state := natHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" {
					t.Error("read mutated API")
				}
				switch q.URL.Path {
				case "/v1/vpcs/" + testVPCID:
					body := natVPC(natBody)
					switch name {
					case "error":
						body = natVPC(strings.Replace(natBody, `"attached"`, `"error"`, 1))
					case "foreign gateway":
						body = natVPC(strings.Replace(natBody, testNATID, testRegionID, 1))
					case "pending release", "fully gone", "IP retained manually":
						body = vpcBody
					case "missing VPC paid IP":
						w.WriteHeader(404)
						body = `{}`
					case "read forbidden":
						w.WriteHeader(403)
						body = `{"detail":"secret marker"}`
					case "malformed read":
						body = `{`
					}
					_, _ = w.Write([]byte(body))
				case "/v1/floating-ips/" + testNATID:
					if name == "fully gone" {
						w.WriteHeader(404)
						_, _ = w.Write([]byte(`{}`))
						return
					}
					body := releasingIPBody
					if name == "IP retained manually" {
						body = strings.Replace(body, `true`, `false`, 1)
					}
					_, _ = w.Write([]byte(body))
				default:
					t.Error("wrong read path")
				}
			})
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			wantError := name == "foreign gateway" || name == "missing VPC paid IP" || name == "IP retained manually" || name == "read forbidden" || name == "malformed read"
			if resp.Diagnostics.HasError() != wantError {
				t.Fatal(resp.Diagnostics)
			}
			if name == "fully gone" {
				if !resp.State.Raw.IsNull() {
					t.Fatal("missing allocation retained")
				}
			} else if natState(t, resp.State).ID.ValueString() != testNATID {
				t.Fatal("paid allocation lost")
			}
			if name == "pending release" && natState(t, resp.State).Status.ValueString() != "detaching" {
				t.Fatal("release not tracked")
			}
		})
	}
}
func TestNATAmbiguousWritesAreNotRetried(t *testing.T) {
	for _, op := range []string{"POST", "DELETE"} {
		t.Run(op, func(t *testing.T) {
			var writes atomic.Int32
			r, plan, state := natHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method == "GET" {
					body := vpcBody
					if op == "DELETE" {
						body = natVPC(natBody)
					}
					_, _ = w.Write([]byte(body))
					return
				}
				writes.Add(1)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			})
			if op == "POST" {
				state.Raw = tftypes.NewValue(state.Raw.Type(), nil)
				resp := resource.CreateResponse{State: state}
				r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
				if !resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
					t.Fatal("ambiguous allocation adopted")
				}
			} else {
				resp := resource.DeleteResponse{State: state}
				r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
				if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
					t.Fatal("ambiguous disable lost state")
				}
			}
			if writes.Load() != 1 {
				t.Fatal("write retried")
			}
		})
	}
}
func TestNATImportValidationAndReplacement(t *testing.T) {
	ctx := context.Background()
	r, plan, state := natHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("local operations called API") })
	for _, id := range []string{testVPCID + "/" + testNATID, testVPCID, "", "bad/" + testNATID, testVPCID + "/" + testNATID + "/extra"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: state.Schema, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		valid := id == testVPCID+"/"+testNATID
		if resp.Diagnostics.HasError() == valid {
			t.Fatal(resp.Diagnostics)
		}
		if valid {
			m := natState(t, resp.State)
			if m.ID.ValueString() != testNATID || m.VPCID.ValueString() != testVPCID {
				t.Fatal("import identity lost")
			}
		}
	}
	for _, value := range []types.String{types.StringValue(testVPCID), types.StringUnknown(), types.StringNull(), types.StringValue("../vpc")} {
		m := natState(t, state)
		m.VPCID = value
		p := plan
		if d := p.Set(ctx, &m); d.HasError() {
			t.Fatal(d)
		}
		resp := resource.ValidateConfigResponse{}
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Raw: p.Raw, Schema: p.Schema}}, &resp)
		valid := value.IsUnknown() || value.ValueString() == testVPCID
		if resp.Diagnostics.HasError() == valid {
			t.Fatal(resp.Diagnostics)
		}
	}
	for _, changed := range []bool{false, true} {
		m := natState(t, state)
		if changed {
			m.VPCID = types.StringValue(testRegionID)
		}
		p := plan
		if d := p.Set(ctx, &m); d.HasError() {
			t.Fatal(d)
		}
		prior, _ := tfprotov6.NewDynamicValue(state.Raw.Type(), state.Raw)
		proposed, _ := tfprotov6.NewDynamicValue(p.Raw.Type(), p.Raw)
		server := providerserver.NewProtocol6(New())()
		resp, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: "tatnet_nat_gateway", PriorState: &prior, ProposedNewState: &proposed, Config: &proposed})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range resp.Diagnostics {
			if d.Severity == tfprotov6.DiagnosticSeverityError {
				t.Fatal(d.Detail)
			}
		}
		if (len(resp.RequiresReplace) > 0) != changed {
			t.Fatal("wrong replacement semantics")
		}
	}
	var update resource.UpdateResponse
	r.Update(ctx, resource.UpdateRequest{}, &update)
	if !update.Diagnostics.HasError() {
		t.Fatal("update allowed")
	}
	for _, client := range []any{nil, "invalid", r.client} {
		resp := resource.ConfigureResponse{}
		target := &natGatewayResource{}
		target.Configure(ctx, resource.ConfigureRequest{ProviderData: client}, &resp)
		if resp.Diagnostics.HasError() != (client == "invalid") {
			t.Fatal(resp.Diagnostics)
		}
	}
}

func TestNATLocalGuardsRetainState(t *testing.T) {
	ctx := context.Background()
	r, plan, state := natHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid local configuration called API") })
	for _, kind := range []string{"missing client", "invalid VPC", "invalid allocation"} {
		t.Run(kind, func(t *testing.T) {
			target := *r
			s := state
			p := plan
			m := natState(t, state)
			switch kind {
			case "missing client":
				target.client = nil
			case "invalid VPC":
				m.VPCID = types.StringValue("../vpc")
			case "invalid allocation":
				m.ID = types.StringValue("../ip")
			}
			if d := s.Set(ctx, &m); d.HasError() {
				t.Fatal(d)
			}
			read := resource.ReadResponse{State: s}
			target.Read(ctx, resource.ReadRequest{State: s}, &read)
			del := resource.DeleteResponse{State: s}
			target.Delete(ctx, resource.DeleteRequest{State: s}, &del)
			if !read.Diagnostics.HasError() || !del.Diagnostics.HasError() || !read.State.Raw.Equal(s.Raw) || !del.State.Raw.Equal(s.Raw) {
				t.Fatal("local guard lost state")
			}
			if kind != "invalid allocation" {
				if d := p.Set(ctx, &m); d.HasError() {
					t.Fatal(d)
				}
				create := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
				target.Create(ctx, resource.CreateRequest{Plan: p}, &create)
				if !create.Diagnostics.HasError() || !create.State.Raw.IsNull() {
					t.Fatal("invalid create claimed a paid allocation")
				}
			}
		})
	}
}
