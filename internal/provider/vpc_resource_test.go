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

func vpcHarness(t *testing.T, handler http.HandlerFunc) (*vpcResource, tfsdk.Plan, tfsdk.State) {
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
	r := &vpcResource{client: c, pollInterval: time.Millisecond, operationTimeout: 100 * time.Millisecond}
	ctx := context.Background()
	var schema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schema)
	m := vpcModel{ID: types.StringUnknown(), ClusterID: types.StringValue(testRegionID), Name: types.StringValue("fixture-network"), Subnet: types.StringValue("10.42.0.0/24"), Status: types.StringUnknown(), IsDefault: types.BoolUnknown()}
	plan := tfsdk.Plan{Schema: schema.Schema}
	if d := plan.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	m.ID = types.StringValue(testVPCID)
	m.Status = types.StringValue("active")
	m.IsDefault = types.BoolValue(false)
	state := tfsdk.State{Schema: schema.Schema}
	if d := state.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	return r, plan, state
}
func vpcState(t *testing.T, state tfsdk.State) vpcModel {
	t.Helper()
	var m vpcModel
	if d := state.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return m
}
func TestVPCResourceCreate(t *testing.T) {
	for _, tc := range []struct {
		name, body        string
		code              int
		wantError, retain bool
	}{
		{"active", vpcBody, 201, false, true},
		{"pending then active", vpcBody, 201, false, true},
		{"provisioning failure", strings.Replace(vpcBody, `"active"`, `"error"`, 1), 201, true, true},
		{"timeout", strings.Replace(vpcBody, `"active"`, `"pending"`, 1), 201, true, true},
		{"missing status", strings.Replace(vpcBody, `,"status":"active"`, "", 1), 201, true, true},
		{"default", strings.Replace(vpcBody, `"is_default":false`, `"is_default":true`, 1), 201, true, true},
		{"missing default", strings.Replace(vpcBody, `,"is_default":false`, "", 1), 201, true, true},
		{"wrong inputs", strings.Replace(vpcBody, "fixture-network", "other", 1), 201, true, true},
		{"malformed read", `{`, 201, true, true},
		{"empty creation", `{}`, 201, true, false},
		{"malformed creation", `{`, 201, true, false},
		{"forbidden", `{"detail":"secret marker"}`, 403, true, false},
		{"rejected subnet", `{"detail":"secret marker"}`, 400, true, false},
		{"server error", `{"detail":"secret marker"}`, 500, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts, gets atomic.Int32
			r, plan, state := vpcHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method == "POST" && q.URL.Path == "/v1/vpcs" {
					posts.Add(1)
					var input tatnet.V1VpcCreate
					if err := json.NewDecoder(q.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					if input.ClusterId != testRegionID || input.Name != "fixture-network" || input.Subnet != "10.42.0.0/24" {
						t.Error("wrong creation payload")
					}
					w.WriteHeader(tc.code)
					if tc.retain {
						_, _ = w.Write([]byte(vpcBody))
					} else {
						_, _ = w.Write([]byte(tc.body))
					}
					return
				}
				if q.Method != "GET" || q.URL.Path != "/v1/vpcs/"+testVPCID {
					t.Errorf("unexpected request %s %s", q.Method, q.URL.Path)
				}
				n := gets.Add(1)
				body := tc.body
				if tc.name == "pending then active" && n == 1 {
					body = strings.Replace(vpcBody, `"active"`, `"pending"`, 1)
				}
				_, _ = w.Write([]byte(body))
			})
			state.Raw = tftypes.NewValue(state.Raw.Type(), nil)
			resp := resource.CreateResponse{State: state}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
			if resp.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("diagnostics: %v", resp.Diagnostics)
			}
			if posts.Load() != 1 {
				t.Fatal("POST retried or omitted")
			}
			if tc.retain {
				if vpcState(t, resp.State).ID.ValueString() != testVPCID {
					t.Fatal("created ID lost")
				}
			} else if !resp.State.Raw.IsNull() {
				t.Fatal("unexpected state")
			}
			if tc.wantError && strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "secret marker") {
				t.Fatal("API body leaked")
			}
			if !tc.wantError && vpcState(t, resp.State).Status.ValueString() != "active" {
				t.Fatal("creation did not wait")
			}
		})
	}
}
func TestVPCResourceReadAndDelete(t *testing.T) {
	for _, tc := range []struct {
		name, body                           string
		readCode, deleteCode                 int
		readError, deleteError, deleteCalled bool
	}{
		{"normal", vpcBody, 200, 204, false, false, true},
		{"absent", `{}`, 404, 0, false, false, false},
		{"default", strings.Replace(vpcBody, `"is_default":false`, `"is_default":true`, 1), 200, 0, false, true, false},
		{"unknown default", strings.Replace(vpcBody, `,"is_default":false`, "", 1), 200, 0, false, true, false},
		{"NAT", strings.Replace(vpcBody, `"is_default":false`, `"is_default":false,"nat_gateway":{"status":"active","fip_id":"fixture","address":"192.0.2.1"}`, 1), 200, 0, false, true, false},
		{"busy", vpcBody, 200, 409, false, true, true},
		{"delete forbidden", vpcBody, 200, 403, false, true, true},
		{"delete server error", vpcBody, 200, 500, false, true, true},
		{"delete raced absence", vpcBody, 200, 404, false, false, true},
		{"read forbidden", `{"detail":"secret marker"}`, 403, 0, true, true, false},
		{"read server error", `{}`, 500, 0, true, true, false},
		{"malformed", `{`, 200, 0, true, true, false},
		{"wrong ID", strings.Replace(vpcBody, testVPCID, testRegionID, 1), 200, 0, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deletes atomic.Int32
			r, _, state := vpcHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.URL.Path != "/v1/vpcs/"+testVPCID {
					t.Error("wrong path")
				}
				switch q.Method {
				case "GET":
					w.WriteHeader(tc.readCode)
					_, _ = w.Write([]byte(tc.body))
				case "DELETE":
					deletes.Add(1)
					w.WriteHeader(tc.deleteCode)
					if tc.deleteCode != 204 {
						_, _ = w.Write([]byte(`{"detail":"secret marker"}`))
					}
				default:
					t.Error("unexpected method")
				}
			})
			rd := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &rd)
			if rd.Diagnostics.HasError() != tc.readError {
				t.Fatalf("read diagnostics %v", rd.Diagnostics)
			}
			if tc.readCode == 404 {
				if !rd.State.Raw.IsNull() {
					t.Fatal("absent resource retained")
				}
			} else if vpcState(t, rd.State).ID.ValueString() != testVPCID {
				t.Fatal("read lost state")
			}
			dd := resource.DeleteResponse{State: state}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &dd)
			if dd.Diagnostics.HasError() != tc.deleteError {
				t.Fatalf("delete diagnostics %v", dd.Diagnostics)
			}
			if (deletes.Load() == 1) != tc.deleteCalled {
				t.Fatalf("unsafe deletion count %d", deletes.Load())
			}
			if tc.deleteError {
				if !dd.State.Raw.Equal(state.Raw) {
					t.Fatal("failed delete changed state")
				}
				if strings.Contains(dd.Diagnostics.Errors()[0].Detail(), "secret marker") {
					t.Fatal("API body leaked")
				}
			}
		})
	}
}
func TestVPCAmbiguousCreateAndDelete(t *testing.T) {
	for _, op := range []string{"create", "delete"} {
		t.Run(op, func(t *testing.T) {
			var writes atomic.Int32
			r, plan, state := vpcHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method == "GET" {
					_, _ = w.Write([]byte(vpcBody))
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
			if op == "create" {
				state.Raw = tftypes.NewValue(state.Raw.Type(), nil)
				resp := resource.CreateResponse{State: state}
				r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
				if !resp.Diagnostics.HasError() {
					t.Fatal("missing diagnostic")
				}
			} else {
				resp := resource.DeleteResponse{State: state}
				r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
				if !resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
					t.Fatal("delete lost state")
				}
			}
			if writes.Load() != 1 {
				t.Fatal("ambiguous write retried")
			}
		})
	}
}
func TestVPCValidationImportAndReplacement(t *testing.T) {
	ctx := context.Background()
	r, plan, state := vpcHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("validation/plan/import called API") })
	for _, tc := range []struct {
		field, value string
		valid        bool
	}{
		{"name", "", false}, {"name", "  ", false}, {"name", strings.Repeat("я", 256), false}, {"name", "сеть", true},
		{"cluster_id", "../region", false}, {"cluster_id", testRegionID, true},
		{"subnet", "10.42.0.1/24", false}, {"subnet", "fd00::/64", false}, {"subnet", "bad", false}, {"subnet", "10.42.0.0/24", true},
	} {
		t.Run(tc.field+tc.value, func(t *testing.T) {
			m := vpcState(t, state)
			switch tc.field {
			case "name":
				m.Name = types.StringValue(tc.value)
			case "cluster_id":
				m.ClusterID = types.StringValue(tc.value)
			case "subnet":
				m.Subnet = types.StringValue(tc.value)
			}
			p := plan
			if d := p.Set(ctx, &m); d.HasError() {
				t.Fatal(d)
			}
			resp := resource.ValidateConfigResponse{}
			r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Raw: p.Raw, Schema: p.Schema}}, &resp)
			if resp.Diagnostics.HasError() == tc.valid {
				t.Fatal("wrong validation result", resp.Diagnostics)
			}
		})
	}
	for _, id := range []string{testVPCID, "", "../vpc", "bad"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: state.Schema, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		if resp.Diagnostics.HasError() != (id != testVPCID) {
			t.Fatal(resp.Diagnostics)
		}
		if id == testVPCID && vpcState(t, resp.State).ID.ValueString() != id {
			t.Fatal("import ID lost")
		}
	}
	for _, field := range []string{"name", "subnet", "cluster_id", "unchanged"} {
		t.Run("replace "+field, func(t *testing.T) {
			m := vpcState(t, state)
			switch field {
			case "name":
				m.Name = types.StringValue("replacement")
			case "subnet":
				m.Subnet = types.StringValue("10.43.0.0/24")
			case "cluster_id":
				m.ClusterID = types.StringValue(testVPCID)
			}
			p := plan
			if d := p.Set(ctx, &m); d.HasError() {
				t.Fatal(d)
			}
			prior, _ := tfprotov6.NewDynamicValue(state.Raw.Type(), state.Raw)
			proposed, _ := tfprotov6.NewDynamicValue(p.Raw.Type(), p.Raw)
			server := providerserver.NewProtocol6(New())()
			res, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: "tatnet_vpc", PriorState: &prior, ProposedNewState: &proposed, Config: &proposed})
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range res.Diagnostics {
				if d.Severity == tfprotov6.DiagnosticSeverityError {
					t.Fatal(d.Detail)
				}
			}
			if (len(res.RequiresReplace) > 0) != (field != "unchanged") {
				t.Fatal("incorrect replacement", res.RequiresReplace)
			}
		})
	}
}

func TestVPCConfigurationAndLocalGuards(t *testing.T) {
	ctx := context.Background()
	r, plan, state := vpcHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("local guard called API") })
	for _, data := range []any{nil, "wrong", r.client} {
		var resp resource.ConfigureResponse
		target := &vpcResource{}
		target.Configure(ctx, resource.ConfigureRequest{ProviderData: data}, &resp)
		if resp.Diagnostics.HasError() != (data == "wrong") {
			t.Fatal(resp.Diagnostics)
		}
	}
	for _, kind := range []string{"missing client", "invalid name", "unknown region"} {
		t.Run(kind, func(t *testing.T) {
			target := *r
			p := plan
			m := vpcState(t, state)
			switch kind {
			case "missing client":
				target.client = nil
			case "invalid name":
				m.Name = types.StringValue("")
			case "unknown region":
				m.ClusterID = types.StringUnknown()
			}
			if d := p.Set(ctx, &m); d.HasError() {
				t.Fatal(d)
			}
			resp := resource.CreateResponse{State: state}
			target.Create(ctx, resource.CreateRequest{Plan: p}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("missing diagnostic")
			}
		})
	}
	for _, kind := range []string{"missing client", "invalid ID"} {
		target := *r
		s := state
		m := vpcState(t, s)
		if kind == "missing client" {
			target.client = nil
		} else {
			m.ID = types.StringValue("../network")
			if d := s.Set(ctx, &m); d.HasError() {
				t.Fatal(d)
			}
		}
		read := resource.ReadResponse{State: s}
		target.Read(ctx, resource.ReadRequest{State: s}, &read)
		del := resource.DeleteResponse{State: s}
		target.Delete(ctx, resource.DeleteRequest{State: s}, &del)
		if !read.Diagnostics.HasError() || !del.Diagnostics.HasError() || !read.State.Raw.Equal(s.Raw) || !del.State.Raw.Equal(s.Raw) {
			t.Fatal("guard lost state")
		}
	}
	update := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, &update)
	if !update.Diagnostics.HasError() || !update.State.Raw.Equal(state.Raw) {
		t.Fatal("unsupported update changed state")
	}
}

func TestVPCCreateReadFailureKeepsIdentity(t *testing.T) {
	for _, code := range []int{403, 404, 500} {
		r, plan, state := vpcHarness(t, func(w http.ResponseWriter, q *http.Request) {
			if q.Method == "POST" {
				w.WriteHeader(201)
				_, _ = w.Write([]byte(vpcBody))
				return
			}
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"detail":"secret marker"}`))
		})
		state.Raw = tftypes.NewValue(state.Raw.Type(), nil)
		resp := resource.CreateResponse{State: state}
		r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
		if !resp.Diagnostics.HasError() || vpcState(t, resp.State).ID.ValueString() != testVPCID {
			t.Fatal("failed reconciliation lost created ID")
		}
	}
}

func TestVPCCreatePreservesPlannedRegionSpelling(t *testing.T) {
	region := "abcdefab-abcd-4abc-8abc-abcdefabcdef"
	body := strings.Replace(vpcBody, testRegionID, region, 1)
	r, plan, state := vpcHarness(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "POST" {
			w.WriteHeader(201)
		}
		_, _ = w.Write([]byte(body))
	})
	m := vpcState(t, state)
	m.ClusterID = types.StringValue(strings.ToUpper(region))
	if d := plan.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	state.Raw = tftypes.NewValue(state.Raw.Type(), nil)
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() || !vpcState(t, resp.State).ClusterID.Equal(m.ClusterID) {
		t.Fatal("created state differs from planned region", resp.Diagnostics)
	}
}

func TestVPCReadPreservesEquivalentRegionUUID(t *testing.T) {
	region := "abcdefab-abcd-4abc-8abc-abcdefabcdef"
	body := strings.Replace(vpcBody, testRegionID, region, 1)
	r, _, state := vpcHarness(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" {
			t.Error("read mutated network")
		}
		_, _ = w.Write([]byte(body))
	})
	m := vpcState(t, state)
	m.ClusterID = types.StringValue(strings.ToUpper(region))
	if d := state.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() || !vpcState(t, resp.State).ClusterID.Equal(m.ClusterID) {
		t.Fatal("refresh changed equivalent configured region", resp.Diagnostics)
	}
}
