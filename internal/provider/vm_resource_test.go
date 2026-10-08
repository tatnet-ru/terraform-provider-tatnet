package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

func testVMModel(t *testing.T) vmModel {
	t.Helper()
	keys, d := types.SetValueFrom(context.Background(), types.StringType, []string{"ssh-key"})
	if d.HasError() {
		t.Fatal(d)
	}
	return vmModel{ID: types.StringUnknown(), ProjectID: types.StringValue("project-a"), ClusterID: types.StringValue("region-a"), ImageID: types.StringValue("image-a"), PlanID: types.StringValue("plan-a"), Name: types.StringValue("test-vm"), Hostname: types.StringValue("test-vm"), DefaultUser: types.StringValue("debian"), VPCID: types.StringValue("vpc-a"), SSHKeys: keys, PeriodDays: types.Int64Value(1), AutoRenew: types.BoolValue(false), Status: types.StringUnknown(), IPv4: types.ListUnknown(types.StringType)}
}
func vmJSON(status string) string {
	return `{"id":"vm-a","project_id":"project-a","name":"test-vm","hostname":"test-vm","status":"` + status + `","vcpu":1,"mem":1024,"disk_size":10,"ipv4_addresses":["10.0.0.2"]}`
}
func vmHarness(t *testing.T, handler http.HandlerFunc) (*vmResource, tfsdk.Plan, tfsdk.State) {
	t.Helper()
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing auth")
		}
		if !strings.HasPrefix(q.URL.Path, "/v1/projects/project-a/vms") {
			t.Errorf("wrong project path: %s", q.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, q)
	}))
	t.Cleanup(server.Close)
	client, err := tatnet.NewClientWithResponses(server.URL+"/v1", tatnet.WithAPIKey("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	r := &vmResource{client: client, pollInterval: time.Millisecond, operationTimeout: 100 * time.Millisecond}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	plan := tfsdk.Plan{Schema: sr.Schema}
	m := testVMModel(t)
	if d := plan.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	state := tfsdk.State{Schema: sr.Schema}
	m.ID = types.StringValue("vm-a")
	m.Status = types.StringValue("running")
	m.IPv4 = types.ListNull(types.StringType)
	if d := state.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	return r, plan, state
}
func TestVMCreate(t *testing.T) {
	for _, tc := range []struct {
		name, initial, next string
		code                int
		failed, hasID       bool
	}{
		{"async ready", "pending", "running", 201, false, true},
		{"production active", "pending", "active", 201, false, true},
		{"payment pending", "payment_pending", "", 201, true, true},
		{"worker error", "pending", "error", 201, true, true},
		{"timeout", "pending", "pending", 201, true, true},
		{"image denied", "", "", 403, true, false},
		{"balance denied", "", "", 402, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creates := 0
			r, plan, _ := vmHarness(t, func(w http.ResponseWriter, q *http.Request) {
				switch q.Method {
				case "POST":
					creates++
					var body tatnet.V1VMCreate
					if err := json.NewDecoder(q.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body.ImageId != "image-a" || body.ClusterId != "region-a" || body.VmPlanId != "plan-a" || body.PeriodDays == nil || *body.PeriodDays != 1 || body.AutoRenew == nil || *body.AutoRenew {
						t.Errorf("wrong create payload: %+v", body)
					}
					if body.Interfaces == nil || len(*body.Interfaces) != 1 || (*body.Interfaces)[0].VpcId == nil || *(*body.Interfaces)[0].VpcId != "vpc-a" || *(*body.Interfaces)[0].FloatingIp {
						t.Error("wrong network payload")
					}
					w.WriteHeader(tc.code)
					if tc.code == 201 {
						_, _ = w.Write([]byte(vmJSON(tc.initial)))
					} else {
						_, _ = w.Write([]byte(`{"detail":"secret"}`))
					}
				case "GET":
					_, _ = w.Write([]byte(vmJSON(tc.next)))
				default:
					t.Errorf("unexpected method %s", q.Method)
				}
			})
			resp := resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema}}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
			if creates != 1 {
				t.Fatalf("POST repeated: %d", creates)
			}
			if resp.Diagnostics.HasError() != tc.failed {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			if tc.hasID {
				var m vmModel
				if d := resp.State.Get(context.Background(), &m); d.HasError() {
					t.Fatal(d)
				}
				if m.ID.ValueString() != "vm-a" {
					t.Fatal("partial VM ID lost")
				}
				if !tc.failed && m.Status.ValueString() != tc.next {
					t.Fatal("returned before ready")
				}
			}
		})
	}
}
func TestVMRead(t *testing.T) {
	for _, code := range []int{200, 404, 403, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			r, _, state := vmHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" {
					t.Fatal(q.Method)
				}
				w.WriteHeader(code)
				if code == 200 {
					_, _ = w.Write([]byte(strings.Replace(vmJSON("stopped"), `"name":"test-vm"`, `"name":"renamed"`, 1)))
				}
			})
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != (code == 403 || code == 500) {
				t.Fatal(resp.Diagnostics)
			}
			if resp.State.Raw.IsNull() != (code == 404) {
				t.Fatal("incorrect state removal")
			}
			if code == 200 {
				var m vmModel
				resp.State.Get(context.Background(), &m)
				if m.Name.ValueString() != "renamed" || m.Status.ValueString() != "stopped" {
					t.Fatal("drift not refreshed")
				}
			}
		})
	}
}
func TestVMDelete(t *testing.T) {
	for _, tc := range []struct {
		name      string
		code      int
		disappear bool
		failed    bool
	}{
		{"async delete", 202, true, false}, {"already gone", 404, true, false}, {"denied", 403, false, true}, {"timeout", 202, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			r, _, state := vmHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method == "DELETE" {
					w.WriteHeader(tc.code)
					_, _ = w.Write([]byte(`{"status":"deleting"}`))
					return
				}
				if q.Method != "GET" {
					t.Fatal(q.Method)
				}
				reads++
				if tc.disappear && reads > 1 {
					w.WriteHeader(404)
					return
				}
				_, _ = w.Write([]byte(vmJSON("deleting")))
			})
			resp := resource.DeleteResponse{State: state}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != tc.failed {
				t.Fatal(resp.Diagnostics)
			}
			if resp.State.Raw.IsNull() == tc.failed {
				t.Fatal("incorrect delete state")
			}
			if tc.code == 202 && !tc.failed && reads < 2 {
				t.Fatal("returned before disappearance")
			}
		})
	}
}

func TestVMPlanRequiresReplacement(t *testing.T) {
	ctx := context.Background()
	_, plan, state := vmHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("planning must not call API") })
	var m vmModel
	if d := plan.Get(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	m.ImageID = types.StringValue("different-image")
	if d := plan.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	server := providerserver.NewProtocol6(New())()
	typ := plan.Raw.Type()
	prior, err := tfprotov6.NewDynamicValue(typ, state.Raw)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := tfprotov6.NewDynamicValue(typ, plan.Raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: "tatnet_vm", PriorState: &prior, ProposedNewState: &proposed, Config: &proposed})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range result.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatal(d.Detail)
		}
	}
	if len(result.RequiresReplace) == 0 {
		t.Fatal("image change must require replacement")
	}
}
