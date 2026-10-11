package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

const kubeWorkerPool = "10000000-0000-4000-8000-000000000006"

func scalingCluster(count int, poolStatus string) string {
	var v tatnet.V1K8sCluster
	_ = json.Unmarshal([]byte(kubeBody), &v)
	(*v.NodePools)[1].DesiredCount = count
	(*v.NodePools)[1].Status = poolStatus
	b, _ := json.Marshal(v)
	return string(b)
}
func scalingNodes(workers int) []tatnet.V1K8sNode {
	result := []tatnet.V1K8sNode{}
	for i := 0; i <= workers; i++ {
		role := "worker"
		if i == 0 {
			role = "controlplane"
		}
		vm := fmt.Sprintf("20000000-0000-4000-8000-%012d", i+1)
		result = append(result, tatnet.V1K8sNode{Id: fmt.Sprintf("30000000-0000-4000-8000-%012d", i+1), VmId: &vm, Role: role, Status: "provisioning"})
	}
	return result
}

func TestKubernetesWorkerScaling(t *testing.T) {
	cases := []struct {
		name   string
		target int
		fail   bool
		posts  int
	}{
		{"up", 3, false, 1}, {"down", 1, false, 1}, {"zero", 0, false, 1}, {"resume", 3, false, 0},
		{"wait pool", 3, false, 1}, {"wait nodes", 3, false, 1}, {"wait excess nodes", 1, false, 1}, {"wait removing nodes", 1, false, 1},
		{"forbidden", 3, true, 1}, {"insufficient funds", 3, true, 1}, {"invalid acknowledgement", 3, true, 1}, {"wrong pool acknowledgement", 3, true, 1},
		{"wrong role acknowledgement", 3, true, 1}, {"wrong count acknowledgement", 3, true, 1}, {"wrong plan acknowledgement", 3, true, 1},
		{"ambiguous POST", 3, true, 1}, {"poll forbidden", 3, true, 1}, {"poll malformed", 3, true, 1}, {"poll drift", 3, true, 1},
		{"pool replaced", 3, true, 1}, {"node request forbidden", 3, true, 1}, {"malformed nodes", 3, true, 1}, {"duplicate nodes", 3, true, 1}, {"duplicate VMs", 3, true, 1},
		{"pool error", 3, true, 1}, {"timeout", 3, true, 1}, {"initial upgrading", 3, true, 0}, {"initial deleting", 3, true, 0},
		{"initial foreign project", 3, true, 0}, {"initial drift", 3, true, 0}, {"initial missing", 3, true, 0}, {"concurrent scale", 3, true, 0}, {"busy pool", 3, true, 0}, {"busy control-plane", 3, true, 0}, {"upgrade during poll", 3, true, 1}, {"delete during poll", 3, true, 1}, {"wait missing VM", 3, false, 1}, {"node error", 3, true, 1}, {"wrong node role", 3, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var posts, gets, nodeGets atomic.Int32
			r, p, s := kubeHarness(t, func(w http.ResponseWriter, q *http.Request) {
				base := "/v1/projects/" + kubeProject + "/kubernetes-clusters/" + kubeID
				switch {
				case q.Method == "POST" && q.URL.Path == base+"/node-pools/"+kubeWorkerPool+"/scale":
					posts.Add(1)
					var payload map[string]int
					if err := json.NewDecoder(q.Body).Decode(&payload); err != nil || len(payload) != 1 || payload["desired_count"] != tc.target {
						t.Error("wrong scale payload", payload, err)
					}
					if tc.name == "ambiguous POST" {
						conn, _, _ := w.(http.Hijacker).Hijack()
						_ = conn.Close()
						return
					}
					if tc.name == "forbidden" || tc.name == "insufficient funds" {
						code := 403
						if tc.name == "insufficient funds" {
							code = 402
						}
						w.WriteHeader(code)
						_, _ = w.Write([]byte(`{"detail":"secret marker"}`))
						return
					}
					var v tatnet.V1K8sCluster
					_ = json.Unmarshal([]byte(scalingCluster(tc.target, "provisioning")), &v)
					pool := (*v.NodePools)[1]
					switch tc.name {
					case "invalid acknowledgement":
						_, _ = w.Write([]byte("{"))
						return
					case "wrong pool acknowledgement":
						pool.Id = kubeID
					case "wrong role acknowledgement":
						pool.Role = "controlplane"
					case "wrong count acknowledgement":
						pool.DesiredCount = tc.target + 1
					case "wrong plan acknowledgement":
						pool.VmPlanId = kubeRegion
					}
					_ = json.NewEncoder(w).Encode(pool)
				case q.Method == "GET" && q.URL.Path == base:
					n := gets.Add(1)
					body := kubeBody
					if n == 1 {
						switch tc.name {
						case "resume":
							body = scalingCluster(tc.target, "provisioning")
						case "initial upgrading":
							body = strings.Replace(body, `"status":"ready"`, `"status":"ready","upgrading":true`, 1)
						case "initial deleting":
							body = strings.Replace(body, `"ready"`, `"deleting"`, 1)
						case "initial foreign project":
							body = strings.Replace(body, kubeProject, kubeRegion, 1)
						case "initial drift":
							body = strings.Replace(body, "fixture-kubernetes", "external-name", 1)
						case "initial missing":
							w.WriteHeader(404)
							return
						case "concurrent scale":
							body = scalingCluster(4, "active")
						case "busy control-plane":
							body = strings.Replace(body, `"status":"active"`, `"status":"error"`, 1)
						case "busy pool":
							body = scalingCluster(2, "scaling")
						}
					} else {
						body = scalingCluster(tc.target, "active")
						switch tc.name {
						case "wait pool":
							if n == 2 {
								body = scalingCluster(tc.target, "provisioning")
							}
						case "pool error":
							body = scalingCluster(tc.target, "error")
						case "timeout":
							body = scalingCluster(tc.target, "provisioning")
						case "poll forbidden":
							w.WriteHeader(403)
							_, _ = w.Write([]byte(`{"detail":"secret marker"}`))
							return
						case "poll malformed":
							body = "{"
						case "poll drift":
							body = scalingCluster(tc.target+1, "active")
						case "upgrade during poll":
							body = strings.Replace(body, `"status":"ready"`, `"status":"ready","upgrading":true`, 1)
						case "delete during poll":
							body = strings.Replace(body, `"ready"`, `"deleting"`, 1)
						case "pool replaced":
							body = strings.Replace(body, kubeWorkerPool, kubeRegion, 1)
						}
					}
					_, _ = w.Write([]byte(body))
				case q.Method == "GET" && q.URL.Path == base+"/nodes":
					n := nodeGets.Add(1)
					nodes := scalingNodes(tc.target)
					switch tc.name {
					case "wait nodes":
						if n == 1 {
							nodes = scalingNodes(tc.target - 1)
						}
					case "wait excess nodes":
						if n == 1 {
							nodes = scalingNodes(tc.target + 1)
						}
					case "wait removing nodes":
						if n == 1 {
							nodes[1].Status = "removing"
						}
					case "wait missing VM":
						if n == 1 {
							nodes[1].VmId = nil
						}
					case "node error":
						nodes[1].Status = "error"
					case "wrong node role":
						nodes[1].Role = "foreign"
					case "node request forbidden":
						w.WriteHeader(403)
						return
					case "malformed nodes":
						_, _ = w.Write([]byte("{"))
						return
					case "duplicate nodes":
						nodes[1].Id = nodes[0].Id
					case "duplicate VMs":
						nodes[1].VmId = nodes[0].VmId
					}
					_ = json.NewEncoder(w).Encode(nodes)
				default:
					t.Error("unexpected mutation/request", q.Method, q.URL.Path)
					w.WriteHeader(500)
				}
			})
			r.operationTimeout = 500 * time.Millisecond
			if tc.name == "timeout" {
				r.operationTimeout = 15 * time.Millisecond
			}
			planned := kubeState(t, s)
			planned.WorkerCount = types.Int64Value(int64(tc.target))
			if d := p.Set(context.Background(), &planned); d.HasError() {
				t.Fatal(d)
			}
			resp := resource.UpdateResponse{State: s}
			r.Update(context.Background(), resource.UpdateRequest{Plan: p, State: s}, &resp)
			if resp.Diagnostics.HasError() != tc.fail {
				t.Fatal(resp.Diagnostics)
			}
			if int(posts.Load()) != tc.posts {
				t.Fatal("unexpected POST count", posts.Load())
			}
			m := kubeState(t, resp.State)
			if m.ID.ValueString() != kubeID || m.ProjectID.ValueString() != kubeProject {
				t.Fatal("cluster identity lost")
			}
			if !tc.fail && m.WorkerCount.ValueInt64() != int64(tc.target) {
				t.Fatal("desired workers not recorded")
			}
			if tc.name == "timeout" || tc.name == "poll forbidden" {
				if m.WorkerCount.ValueInt64() != int64(tc.target) {
					t.Fatal("acknowledged scale intent lost")
				}
			}
			for _, d := range resp.Diagnostics {
				if strings.Contains(d.Detail(), "secret marker") {
					t.Fatal("response body leaked")
				}
			}
			if strings.HasPrefix(tc.name, "wait ") && gets.Load() < 3 {
				t.Fatal("scaling returned before convergence")
			}
		})
	}
}
func TestKubernetesScalingRejectsImmutableUpdatesLocally(t *testing.T) {
	for _, field := range []string{"name", "control_plane_count", "unknown_workers", "bad_workers"} {
		t.Run(field, func(t *testing.T) {
			r, p, s := kubeHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid input reached API") })
			m := kubeState(t, s)
			switch field {
			case "name":
				m.Name = types.StringValue("changed")
			case "control_plane_count":
				m.ControlPlaneCount = types.Int64Value(3)
			case "unknown_workers":
				m.WorkerCount = types.Int64Unknown()
			case "bad_workers":
				m.WorkerCount = types.Int64Value(-1)
			}
			if d := p.Set(context.Background(), &m); d.HasError() {
				t.Fatal(d)
			}
			resp := resource.UpdateResponse{State: s}
			r.Update(context.Background(), resource.UpdateRequest{Plan: p, State: s}, &resp)
			if !resp.Diagnostics.HasError() || kubeState(t, resp.State).ID.ValueString() != kubeID {
				t.Fatal("unsafe update accepted or identity lost")
			}
		})
	}
}
