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

const kubeProject = "10000000-0000-4000-8000-000000000001"
const kubeID = "10000000-0000-4000-8000-000000000002"
const kubeRegion = "10000000-0000-4000-8000-000000000003"
const kubePlan = "10000000-0000-4000-8000-000000000004"
const kubeBody = `{"id":"` + kubeID + `","project_id":"` + kubeProject + `","region_id":"` + kubeRegion + `","name":"fixture-kubernetes","k8s_version":"1.34.9","talos_version":"1.12.4","endpoint_access":"private","endpoint_port":6443,"internal_vip":"10.42.0.10","status":"ready","node_pools":[{"id":"10000000-0000-4000-8000-000000000005","name":"control-plane","role":"controlplane","vm_plan_id":"` + kubePlan + `","desired_count":1,"status":"active"},{"id":"10000000-0000-4000-8000-000000000006","name":"workers","role":"worker","vm_plan_id":"` + kubePlan + `","desired_count":2,"status":"active"}]}`

func kubeHarness(t *testing.T, h http.HandlerFunc) (*kubernetesClusterResource, tfsdk.Plan, tfsdk.State) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get("Authorization") != "Bearer test-key" || q.URL.RawQuery != "" {
			t.Error("unexpected authentication/query")
		}
		w.Header().Set("Content-Type", "application/json")
		h(w, q)
	}))
	t.Cleanup(s.Close)
	c, err := tatnet.NewClientWithResponses(s.URL+"/v1", tatnet.WithAPIKey("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	r := &kubernetesClusterResource{client: c, pollInterval: time.Millisecond, operationTimeout: 60 * time.Millisecond}
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	m := kubernetesClusterModel{ID: types.StringUnknown(), ProjectID: types.StringValue(kubeProject), RegionID: types.StringValue(kubeRegion), VMPlanID: types.StringValue(kubePlan), Name: types.StringValue("fixture-kubernetes"), K8sVersion: types.StringValue("1.34.9"), TalosVersion: types.StringValue("1.12.4"), VPCID: types.StringNull(), EndpointAccess: types.StringValue("private"), ControlPlaneCount: types.Int64Value(1), WorkerCount: types.Int64Value(2), WorkerDiskSizeGB: types.Int64Null(), Status: types.StringUnknown(), EndpointDNS: types.StringUnknown(), EndpointIP: types.StringUnknown(), InternalVIP: types.StringUnknown(), EndpointPort: types.Int64Unknown()}
	p := tfsdk.Plan{Schema: sr.Schema}
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	m.ID = types.StringValue(kubeID)
	var v tatnet.V1K8sCluster
	if err = json.Unmarshal([]byte(kubeBody), &v); err != nil {
		t.Fatal(err)
	}
	if err = m.observe(&v); err != nil {
		t.Fatal(err)
	}
	state := tfsdk.State{Schema: sr.Schema}
	if d := state.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return r, p, state
}
func kubeState(t *testing.T, s tfsdk.State) kubernetesClusterModel {
	t.Helper()
	var m kubernetesClusterModel
	if d := s.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return m
}
func TestKubernetesCreate(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		code         int
		fail, retain bool
	}{
		{"ready", kubeBody, 201, false, true},
		{"pending then ready", kubeBody, 201, false, true},
		{"pending worker then ready", kubeBody, 201, false, true},
		{"worker error", strings.Replace(kubeBody, `"status":"active"`, `"status":"error"`, 1), 201, true, true},
		{"error", strings.Replace(kubeBody, `"ready"`, `"error"`, 1), 201, true, true},
		{"timeout", strings.Replace(kubeBody, `"ready"`, `"bootstrapping"`, 1), 201, true, true},
		{"wrong project", strings.Replace(kubeBody, kubeProject, kubeRegion, 1), 201, true, true},
		{"wrong inputs", strings.Replace(kubeBody, "fixture-kubernetes", "different", 1), 201, true, true},
		{"invalid pool shape", strings.Replace(kubeBody, `"role":"worker"`, `"role":"controlplane"`, 1), 201, true, true},
		{"malformed poll", "{", 201, true, true},
		{"empty creation", "{}", 201, true, false},
		{"malformed creation", "{", 201, true, false},
		{"forbidden", `{"detail":"secret marker"}`, 403, true, false},
		{"server error", `{"detail":"secret marker"}`, 500, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts, gets atomic.Int32
			r, p, s := kubeHarness(t, func(w http.ResponseWriter, q *http.Request) {
				switch q.Method + " " + q.URL.Path {
				case "POST /v1/projects/" + kubeProject + "/kubernetes-clusters":
					posts.Add(1)
					var b tatnet.V1K8sClusterCreate
					if err := json.NewDecoder(q.Body).Decode(&b); err != nil {
						t.Error(err)
					}
					if b.Name != "fixture-kubernetes" || b.VmPlanId != kubePlan || b.RegionId == nil || *b.RegionId != kubeRegion || b.EndpointAccess == nil || *b.EndpointAccess != "private" || b.K8sVersion == nil || *b.K8sVersion != "1.34.9" || b.TalosVersion == nil || *b.TalosVersion != "1.12.4" || b.ControlPlaneCount == nil || *b.ControlPlaneCount != 1 || b.WorkerCount == nil || *b.WorkerCount != 2 || b.VpcId != nil || b.WorkerDiskSizeGb != nil {
						t.Error("wrong create payload")
					}
					w.WriteHeader(tc.code)
					if tc.retain {
						_, _ = w.Write([]byte(kubeBody))
					} else {
						_, _ = w.Write([]byte(tc.body))
					}
				case "GET /v1/projects/" + kubeProject + "/kubernetes-clusters/" + kubeID:
					n := gets.Add(1)
					body := tc.body
					if n == 1 && tc.name == "pending then ready" {
						body = strings.Replace(kubeBody, `"ready"`, `"provisioning"`, 1)
					}
					if n == 1 && tc.name == "pending worker then ready" {
						body = strings.Replace(kubeBody, `"status":"active"`, `"status":"provisioning"`, 1)
					}
					_, _ = w.Write([]byte(body))
				default:
					t.Error("unexpected API call", q.Method, q.URL.Path)
					w.WriteHeader(500)
				}
			})
			resp := resource.CreateResponse{State: s}
			r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
			if resp.Diagnostics.HasError() != tc.fail {
				t.Fatal(resp.Diagnostics)
			}
			if posts.Load() != 1 {
				t.Fatal("POST retried")
			}
			if tc.retain && kubeState(t, resp.State).ID.ValueString() != kubeID {
				t.Fatal("created ID lost")
			}
			for _, d := range resp.Diagnostics {
				if strings.Contains(d.Detail(), "secret marker") {
					t.Fatal("response leaked")
				}
			}
			if strings.HasPrefix(tc.name, "pending") && gets.Load() < 2 {
				t.Fatal("returned before ready")
			}
		})
	}
}
func TestKubernetesRead(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		code         int
		fail, remove bool
	}{
		{"valid", kubeBody, 200, false, false}, {"missing", `{}`, 404, false, true}, {"forbidden", `{"detail":"secret marker"}`, 403, true, false}, {"server error", `{}`, 500, true, false}, {"wrong id", strings.Replace(kubeBody, kubeID, kubeRegion, 1), 200, true, false}, {"wrong project", strings.Replace(kubeBody, kubeProject, kubeRegion, 1), 200, true, false}, {"empty", `{}`, 200, true, false}, {"bad json", `{`, 200, true, false}, {"mixed plans", strings.Replace(kubeBody, `"vm_plan_id":"`+kubePlan+`"`, `"vm_plan_id":"`+kubeRegion+`"`, 1), 200, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, s := kubeHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" {
					t.Error("read wrote to API")
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			})
			resp := resource.ReadResponse{State: s}
			r.Read(context.Background(), resource.ReadRequest{State: s}, &resp)
			if resp.Diagnostics.HasError() != tc.fail || resp.State.Raw.IsNull() != tc.remove {
				t.Fatal(resp.Diagnostics)
			}
			if tc.fail && kubeState(t, resp.State).ID.ValueString() != kubeID {
				t.Fatal("state lost")
			}
		})
	}
}
func TestKubernetesDelete(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		fail bool
		pre  string
	}{
		{"accepted then absent", 202, false, "ready"}, {"delete 404", 404, false, "ready"}, {"already absent", 404, false, "absent"}, {"already deleting", 202, false, "deleting"}, {"forbidden read", 403, true, "denied"}, {"forbidden delete", 403, true, "ready"}, {"timeout", 202, true, "timeout"}, {"poll forbidden", 202, true, "poll denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deletes, gets atomic.Int32
			r, _, s := kubeHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method == "DELETE" {
					deletes.Add(1)
					w.WriteHeader(tc.code)
					if tc.code == 202 {
						_, _ = w.Write([]byte(`{"status":"deleting"}`))
					}
					return
				}
				n := gets.Add(1)
				if tc.pre == "denied" {
					w.WriteHeader(403)
					return
				}
				if tc.pre == "absent" || (n > 1 && tc.pre != "timeout" && tc.pre != "poll denied") {
					w.WriteHeader(404)
					return
				}
				if n > 1 && tc.pre == "poll denied" {
					w.WriteHeader(403)
					return
				}
				body := kubeBody
				if tc.pre == "deleting" {
					body = strings.Replace(body, `"ready"`, `"deleting"`, 1)
				}
				_, _ = w.Write([]byte(body))
			})
			resp := resource.DeleteResponse{State: s}
			r.Delete(context.Background(), resource.DeleteRequest{State: s}, &resp)
			if resp.Diagnostics.HasError() != tc.fail {
				t.Fatal(resp.Diagnostics)
			}
			if deletes.Load() > 1 {
				t.Fatal("DELETE retried")
			}
			if (tc.pre == "absent" || tc.pre == "deleting" || tc.pre == "denied") && deletes.Load() != 0 {
				t.Fatal("unexpected DELETE")
			}
			if tc.fail && kubeState(t, resp.State).ID.ValueString() != kubeID {
				t.Fatal("state lost")
			}
		})
	}
}
func TestKubernetesValidationAndImport(t *testing.T) {
	ctx := context.Background()
	r, p, s := kubeHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("local validation called API") })
	for _, change := range []string{"empty name", "bad UUID", "bad version", "wrong endpoint", "public VPC", "bad cp", "bad workers", "bad disk", "unknown", "unknown optional"} {
		m := kubeState(t, s)
		switch change {
		case "empty name":
			m.Name = types.StringValue(" ")
		case "bad UUID":
			m.ProjectID = types.StringValue("../project")
		case "bad version":
			m.K8sVersion = types.StringValue("latest")
		case "wrong endpoint":
			m.EndpointAccess = types.StringValue("bad")
		case "public VPC":
			m.VPCID = types.StringValue(kubeRegion)
			m.EndpointAccess = types.StringValue("public")
		case "bad cp":
			m.ControlPlaneCount = types.Int64Value(2)
		case "bad workers":
			m.WorkerCount = types.Int64Value(51)
		case "bad disk":
			m.WorkerDiskSizeGB = types.Int64Value(9)
		case "unknown":
			m.RegionID = types.StringUnknown()
		case "unknown optional":
			m.VPCID = types.StringUnknown()
		}
		if err := validateKubernetesConfig(m, true); err == nil {
			t.Fatal("validation accepted", change)
		}
	}
	for _, id := range []string{kubeProject + "/" + kubeID, kubeID, "../x/y", kubeProject + "/" + kubeID + "/extra"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		valid := id == kubeProject+"/"+kubeID
		if resp.Diagnostics.HasError() == valid {
			t.Fatal(resp.Diagnostics)
		}
		if valid {
			m := kubeState(t, resp.State)
			if m.ID.ValueString() != kubeID || m.ProjectID.ValueString() != kubeProject {
				t.Fatal("import scope lost")
			}
		}
	}
	for _, field := range []string{"name", "worker_count", "k8s_version", "vpc_id", "unchanged"} {
		m := kubeState(t, s)
		switch field {
		case "name":
			m.Name = types.StringValue("new-name")
		case "worker_count":
			m.WorkerCount = types.Int64Value(3)
		case "k8s_version":
			m.K8sVersion = types.StringValue("1.35.0")
		case "vpc_id":
			m.VPCID = types.StringValue(kubeRegion)
		}
		p2 := p
		if d := p2.Set(ctx, &m); d.HasError() {
			t.Fatal(d)
		}
		prior, _ := tfprotov6.NewDynamicValue(s.Raw.Type(), s.Raw)
		proposed, _ := tfprotov6.NewDynamicValue(p2.Raw.Type(), p2.Raw)
		server := providerserver.NewProtocol6(New())()
		res, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: "tatnet_kubernetes_cluster", PriorState: &prior, ProposedNewState: &proposed, Config: &proposed})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range res.Diagnostics {
			if d.Severity == tfprotov6.DiagnosticSeverityError {
				t.Fatal(d.Detail)
			}
		}
		if (len(res.RequiresReplace) > 0) != (field != "unchanged") {
			t.Fatal("replacement policy", field, res.RequiresReplace)
		}
	}
}

func TestKubernetesAmbiguousWritesNotRetried(t *testing.T) {
	for _, operation := range []string{"create", "delete"} {
		t.Run(operation, func(t *testing.T) {
			var writes atomic.Int32
			r, p, s := kubeHarness(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method == "GET" {
					_, _ = w.Write([]byte(kubeBody))
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
			if operation == "create" {
				resp := resource.CreateResponse{State: s}
				r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
				if !resp.Diagnostics.HasError() {
					t.Fatal("ambiguous create accepted")
				}
			} else {
				resp := resource.DeleteResponse{State: s}
				r.Delete(context.Background(), resource.DeleteRequest{State: s}, &resp)
				if !resp.Diagnostics.HasError() || kubeState(t, resp.State).ID.ValueString() != kubeID {
					t.Fatal("ambiguous delete lost state")
				}
			}
			if writes.Load() != 1 {
				t.Fatal("ambiguous write retried", writes.Load())
			}
		})
	}
}
func TestKubernetesImportReadAndLocalGuards(t *testing.T) {
	ctx := context.Background()
	r, p, s := kubeHarness(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" {
			t.Error("import read wrote")
		}
		_, _ = w.Write([]byte(kubeBody))
	})
	imported := resource.ImportStateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Raw.Type(), nil)}}
	r.ImportState(ctx, resource.ImportStateRequest{ID: kubeProject + "/" + kubeID}, &imported)
	read := resource.ReadResponse{State: imported.State}
	r.Read(ctx, resource.ReadRequest{State: imported.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	if !kubernetesInputsEqual(kubeState(t, s), kubeState(t, read.State)) {
		t.Fatal("import failed to reconstruct creation inputs")
	}
	for _, providerData := range []any{nil, "wrong", r.client} {
		target := &kubernetesClusterResource{}
		var resp resource.ConfigureResponse
		target.Configure(ctx, resource.ConfigureRequest{ProviderData: providerData}, &resp)
		if resp.Diagnostics.HasError() != (providerData == "wrong") {
			t.Fatal(resp.Diagnostics)
		}
	}
	target := *r
	target.client = nil
	created := resource.CreateResponse{State: s}
	target.Create(ctx, resource.CreateRequest{Plan: p}, &created)
	if !created.Diagnostics.HasError() {
		t.Fatal("missing client accepted")
	}
	update := resource.UpdateResponse{}
	r.Update(ctx, resource.UpdateRequest{}, &update)
	if !update.Diagnostics.HasError() {
		t.Fatal("unexpected in-place update")
	}
}

func TestKubernetesTalosVersionPrefix(t *testing.T) {
	var v tatnet.V1K8sCluster
	if err := json.Unmarshal([]byte(strings.ReplaceAll(kubeBody, "1.12.4", "v1.12.4")), &v); err != nil {
		t.Fatal(err)
	}
	var m kubernetesClusterModel
	if err := m.observe(&v); err != nil {
		t.Fatal(err)
	}
	if m.TalosVersion.ValueString() != "v1.12.4" {
		t.Fatal("Talos prefix lost")
	}
	if err := validateKubernetesConfig(m, true); err != nil {
		t.Fatal(err)
	}
	m.K8sVersion = types.StringValue("v1.34.9")
	if err := validateKubernetesConfig(m, true); err == nil {
		t.Fatal("Kubernetes version accepts a Talos-only prefix")
	}
}
