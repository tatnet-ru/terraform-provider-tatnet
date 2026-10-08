package provider

import (
	"context"
	"encoding/json"
	"fmt"
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

func fipJSON(status, iface string) string {
	var id *string
	if iface != "" {
		id = &iface
	}
	b, _ := json.Marshal(map[string]any{"id": "fip-a", "cluster_id": "region-a", "address": "192.0.2.10", "name": "test-ip", "status": status, "vm_interface_id": id, "target_kind": "interface", "auto_release": false})
	return string(b)
}
func fipHarness(t *testing.T, handler http.HandlerFunc) (*floatingIPResource, tfsdk.Plan, tfsdk.State) {
	t.Helper()
	// A separate server enforces the account scope of networking requests.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing authentication")
		}
		if !strings.HasPrefix(q.URL.Path, "/v1/floating-ips") {
			t.Error("wrong account-scoped path", q.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, q)
	}))
	t.Cleanup(server.Close)
	// Use the generated SDK client rather than mocking resource methods.
	newClient, err := tatnet.NewClientWithResponses(server.URL+"/v1", tatnet.WithAPIKey("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	r := &floatingIPResource{client: newClient, pollInterval: time.Millisecond, operationTimeout: 30 * time.Millisecond}
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	m := floatingIPModel{ID: types.StringUnknown(), ClusterID: types.StringValue("region-a"), Name: types.StringValue("test-ip"), InterfaceID: types.StringValue("nic-a"), Address: types.StringUnknown(), Status: types.StringUnknown()}
	plan := tfsdk.Plan{Schema: sr.Schema}
	if d := plan.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	m.ID = types.StringValue("fip-a")
	m.Address = types.StringValue("192.0.2.10")
	m.Status = types.StringValue("attached")
	state := tfsdk.State{Schema: sr.Schema}
	if d := state.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return r, plan, state
}
func fipState(t *testing.T, state tfsdk.State) floatingIPModel {
	t.Helper()
	var m floatingIPModel
	if d := state.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return m
}

func TestFloatingIPCreate(t *testing.T) {
	for _, tc := range []struct {
		name         string
		code         int
		status       string
		fail, retain bool
	}{
		{"attached", 201, "attached", false, true}, {"worker error", 201, "error", true, true}, {"timeout", 201, "attaching", true, true}, {"forbidden", 403, "", true, false}, {"balance", 402, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creates, attaches := 0, 0
			r, plan, _ := fipHarness(t, func(w http.ResponseWriter, q *http.Request) {
				switch q.Method + " " + q.URL.Path {
				case "POST /v1/floating-ips":
					creates++
					var body map[string]any
					_ = json.NewDecoder(q.Body).Decode(&body)
					if body["cluster_id"] != "region-a" || body["name"] != "test-ip" || len(body) != 2 {
						t.Error("incorrect allocation payload")
					}
					w.WriteHeader(tc.code)
					if tc.code == 201 {
						fmt.Fprint(w, fipJSON("available", ""))
					} else {
						fmt.Fprint(w, `{"detail":"private-backend-secret"}`)
					}
				case "POST /v1/floating-ips/fip-a/attach":
					attaches++
					var body map[string]string
					_ = json.NewDecoder(q.Body).Decode(&body)
					if body["vm_interface_id"] != "nic-a" {
						t.Error("wrong interface")
					}
					fmt.Fprint(w, fipJSON("attaching", "nic-a"))
				case "GET /v1/floating-ips/fip-a":
					fmt.Fprint(w, fipJSON(tc.status, "nic-a"))
				default:
					t.Error("unexpected request", q.Method, q.URL.Path)
					w.WriteHeader(500)
				}
			})
			resp := resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema}}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
			if resp.Diagnostics.HasError() != tc.fail {
				t.Fatal(resp.Diagnostics)
			}
			if creates != 1 {
				t.Fatal("allocation retried", creates)
			}
			if strings.Contains(fmt.Sprint(resp.Diagnostics), "private-backend-secret") {
				t.Fatal("API body leaked")
			}
			if tc.retain {
				m := fipState(t, resp.State)
				if m.ID.ValueString() != "fip-a" || m.Address.ValueString() != "192.0.2.10" {
					t.Fatal("allocation lost")
				}
				if attaches != 1 {
					t.Fatal("attachment retried")
				}
			}
		})
	}
}
func TestFloatingIPRead(t *testing.T) {
	for _, tc := range []struct {
		name          string
		code          int
		body          string
		fail, missing bool
	}{
		{"drift detached", 200, fipJSON("available", ""), false, false},
		{"missing", 404, "", false, true}, {"forbidden", 403, "", true, false}, {"server error", 500, "", true, false},
		{"malformed", 200, "{", true, false}, {"empty", 200, "{}", true, false},
		{"wrong identity", 200, strings.Replace(fipJSON("attached", "nic-a"), "fip-a", "fip-b", 1), true, false},
		{"nat address", 200, strings.Replace(fipJSON("available", ""), `"target_kind":"interface"`, `"target_kind":"vpc_nat"`, 1), true, false},
		{"auto release", 200, strings.Replace(fipJSON("available", ""), `"auto_release":false`, `"auto_release":true`, 1), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, state := fipHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" {
					t.Error("read mutated resource")
				}
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			})
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != tc.fail || resp.State.Raw.IsNull() != tc.missing {
				t.Fatal(resp.Diagnostics, "incorrect state retention")
			}
			if tc.name == "drift detached" && !fipState(t, resp.State).InterfaceID.IsNull() {
				t.Fatal("attachment drift ignored")
			}
		})
	}
}
func TestFloatingIPReattachAndClearName(t *testing.T) {
	events := []string{}
	status, iface := "attached", "nic-a"
	var name any = "test-ip"
	r, plan, state := fipHarness(t, func(w http.ResponseWriter, q *http.Request) {
		events = append(events, q.Method+" "+q.URL.Path)
		switch q.Method + " " + q.URL.Path {
		case "PATCH /v1/floating-ips/fip-a":
			var body map[string]any
			_ = json.NewDecoder(q.Body).Decode(&body)
			if body["name"] != nil {
				t.Error("name not cleared")
			}
			name = nil
		case "POST /v1/floating-ips/fip-a/detach":
			status, iface = "detaching", ""
		case "POST /v1/floating-ips/fip-a/attach":
			if status != "available" {
				t.Error("attached before detach completed")
			}
			status, iface = "attaching", "nic-b"
		case "GET /v1/floating-ips/fip-a":
			if status == "detaching" {
				status = "available"
			} else if status == "attaching" {
				status = "attached"
			}
		default:
			t.Error("unexpected request", q.Method, q.URL.Path)
			w.WriteHeader(500)
			return
		}
		var body map[string]any
		_ = json.Unmarshal([]byte(fipJSON(status, iface)), &body)
		body["name"] = name
		_ = json.NewEncoder(w).Encode(body)
	})
	m := fipState(t, state)
	m.InterfaceID = types.StringValue("nic-b")
	m.Name = types.StringNull()
	plan.Set(context.Background(), &m)
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	got := fipState(t, resp.State)
	if got.ID.ValueString() != "fip-a" || got.InterfaceID.ValueString() != "nic-b" || !got.Name.IsNull() {
		t.Fatal("wrong updated state")
	}
	want := "GET /v1/floating-ips/fip-a|PATCH /v1/floating-ips/fip-a|POST /v1/floating-ips/fip-a/detach|GET /v1/floating-ips/fip-a|POST /v1/floating-ips/fip-a/attach|GET /v1/floating-ips/fip-a"
	if strings.Join(events, "|") != want {
		t.Fatal(events)
	}
}
func TestFloatingIPPartialUpdateRetainsDetachedAllocation(t *testing.T) {
	iface := "nic-a"
	status := "attached"
	r, plan, state := fipHarness(t, func(w http.ResponseWriter, q *http.Request) {
		if strings.HasSuffix(q.URL.Path, "/detach") {
			iface, status = "", "available"
		}
		if strings.HasSuffix(q.URL.Path, "/attach") {
			w.WriteHeader(409)
			return
		}
		fmt.Fprint(w, fipJSON(status, iface))
	})
	m := fipState(t, state)
	m.InterfaceID = types.StringValue("nic-b")
	plan.Set(context.Background(), &m)
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected attachment failure")
	}
	m = fipState(t, resp.State)
	if m.ID.ValueString() != "fip-a" || !m.InterfaceID.IsNull() {
		t.Fatal("partial state lost")
	}
}
func TestFloatingIPDelete(t *testing.T) {
	for _, tc := range []struct {
		name, status, iface string
		getCode, deleteCode int
		fail                bool
	}{
		{"attached", "attached", "nic-a", 200, 204, false}, {"already detached", "available", "", 200, 204, false},
		{"already missing", "", "", 404, 0, false}, {"denied read", "", "", 403, 0, true},
		{"release denied", "available", "", 200, 403, true}, {"network timeout", "detaching", "", 200, 0, true},
		{"error attached cleanup", "error", "nic-a", 200, 204, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, iface := tc.status, tc.iface
			released := false
			r, _, state := fipHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method == "DELETE" {
					if status != "available" || iface != "" {
						t.Error("released before detached")
					}
					released = true
					w.WriteHeader(tc.deleteCode)
					return
				}
				if strings.HasSuffix(q.URL.Path, "/detach") {
					status, iface = "available", ""
				}
				w.WriteHeader(tc.getCode)
				if tc.getCode == 200 {
					fmt.Fprint(w, fipJSON(status, iface))
				}
			})
			resp := resource.DeleteResponse{State: state}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != tc.fail || resp.State.Raw.IsNull() == tc.fail {
				t.Fatal(resp.Diagnostics, "incorrect deletion state")
			}
			if released != (tc.deleteCode != 0) {
				t.Fatal("unexpected release")
			}
		})
	}
}
func TestFloatingIPImportAndPlan(t *testing.T) {
	ctx := context.Background()
	r, plan, state := fipHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("plan/import called API") })
	imported := resource.ImportStateResponse{State: tfsdk.State{Schema: state.Schema, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "fip-a"}, &imported)
	if imported.Diagnostics.HasError() || fipState(t, imported.State).ID.ValueString() != "fip-a" {
		t.Fatal(imported.Diagnostics)
	}
	for _, field := range []string{"cluster", "name", "interface"} {
		t.Run(field, func(t *testing.T) {
			m := fipState(t, state)
			switch field {
			case "cluster":
				m.ClusterID = types.StringValue("region-b")
			case "name":
				m.Name = types.StringValue("renamed")
			case "interface":
				m.InterfaceID = types.StringValue("nic-b")
			}
			if d := plan.Set(ctx, &m); d.HasError() {
				t.Fatal(d)
			}
			prior, _ := tfprotov6.NewDynamicValue(state.Raw.Type(), state.Raw)
			proposed, _ := tfprotov6.NewDynamicValue(plan.Raw.Type(), plan.Raw)
			server := providerserver.NewProtocol6(New())()
			res, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: "tatnet_floating_ip", PriorState: &prior, ProposedNewState: &proposed, Config: &proposed})
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range res.Diagnostics {
				if d.Severity == tfprotov6.DiagnosticSeverityError {
					t.Fatal(d.Detail)
				}
			}
			if (len(res.RequiresReplace) > 0) != (field == "cluster") {
				t.Fatal("incorrect replacement semantics", res.RequiresReplace)
			}
		})
	}
}

func TestFloatingIPAmbiguousAllocationIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	r, plan, _ := fipHarness(t, func(w http.ResponseWriter, q *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	resp := resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() || calls.Load() != 1 {
		t.Fatal("ambiguous allocation was retried", calls.Load(), resp.Diagnostics)
	}
}
func TestFloatingIPUnattachedAllocation(t *testing.T) {
	calls := 0
	r, plan, _ := fipHarness(t, func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method != "POST" || q.URL.Path != "/v1/floating-ips" {
			t.Error("unexpected attachment")
		}
		var body map[string]any
		_ = json.NewDecoder(q.Body).Decode(&body)
		if body["name"] != nil {
			t.Error("unexpected name")
		}
		w.WriteHeader(201)
		fmt.Fprint(w, strings.Replace(fipJSON("available", ""), `"name":"test-ip"`, `"name":null`, 1))
	})
	var m floatingIPModel
	plan.Get(context.Background(), &m)
	m.Name = types.StringNull()
	m.InterfaceID = types.StringNull()
	plan.Set(context.Background(), &m)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() || calls != 1 {
		t.Fatal(resp.Diagnostics, calls)
	}
	m = fipState(t, resp.State)
	if !m.Name.IsNull() || !m.InterfaceID.IsNull() || m.Status.ValueString() != "available" {
		t.Fatal("incorrect unattached state")
	}
}
func TestFloatingIPRemoveAttachment(t *testing.T) {
	iface, status := "nic-a", "attached"
	detaches := 0
	r, plan, state := fipHarness(t, func(w http.ResponseWriter, q *http.Request) {
		switch q.Method + " " + q.URL.Path {
		case "POST /v1/floating-ips/fip-a/detach":
			detaches++
			iface, status = "", "detaching"
		case "GET /v1/floating-ips/fip-a":
			if status == "detaching" {
				status = "available"
			}
		default:
			t.Error("detach must not release allocation", q.Method, q.URL.Path)
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, fipJSON(status, iface))
	})
	m := fipState(t, state)
	m.InterfaceID = types.StringNull()
	plan.Set(context.Background(), &m)
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() || detaches != 1 {
		t.Fatal(resp.Diagnostics, detaches)
	}
	m = fipState(t, resp.State)
	if m.ID.ValueString() != "fip-a" || !m.InterfaceID.IsNull() {
		t.Fatal("allocation lost")
	}
}
func TestFloatingIPConfigurationValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value types.String
		fail  bool
	}{
		{"empty", types.StringValue(""), true}, {"too long", types.StringValue(strings.Repeat("a", 256)), true},
		{"optional", types.StringNull(), false}, {"unknown", types.StringUnknown(), false},
		{"unicode", types.StringValue(strings.Repeat("я", 255)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, plan, _ := fipHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("validation called API") })
			var m floatingIPModel
			plan.Get(context.Background(), &m)
			m.Name = tc.value
			plan.Set(context.Background(), &m)
			resp := resource.ValidateConfigResponse{}
			r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: plan.Schema, Raw: plan.Raw}}, &resp)
			if resp.Diagnostics.HasError() != tc.fail {
				t.Fatal(resp.Diagnostics)
			}
		})
	}
}
