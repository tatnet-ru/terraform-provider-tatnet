package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

func upgradeCluster(version string, upgrading bool, pool string) string {
	var v tatnet.V1K8sCluster
	_ = json.Unmarshal([]byte(kubeBody), &v)
	v.K8sVersion = version
	v.Upgrading = &upgrading
	(*v.NodePools)[1].Status = pool
	b, _ := json.Marshal(v)
	return string(b)
}
func TestKubernetesVersionUpgrade(t *testing.T) {
	cases := []struct {
		name  string
		fail  bool
		posts int
	}{
		{"success", false, 1}, {"resume rolling", false, 0}, {"resume completed", false, 0}, {"wait pools", false, 1}, {"wait nodes", false, 1},
		{"timeout", true, 1}, {"forbidden", true, 1}, {"unsupported target", true, 1}, {"conflict", true, 1}, {"ambiguous POST", true, 1},
		{"invalid acknowledgement", true, 1}, {"foreign acknowledgement", true, 1}, {"wrong target acknowledgement", true, 1}, {"missing progress acknowledgement", true, 1},
		{"poll forbidden", true, 1}, {"poll malformed", true, 1}, {"poll drift", true, 1}, {"poll missing progress", true, 1}, {"pool replaced", true, 1}, {"poll deleting", true, 1}, {"pool error", true, 1},
		{"initial busy", true, 0}, {"initial scaling", true, 0}, {"initial missing progress", true, 0}, {"initial drift", true, 0}, {"initial concurrent version", true, 0}, {"initial missing", true, 0}, {"initial nodes forbidden", true, 0}, {"control-plane replaced", true, 1}, {"invalid endpoint acknowledgement", true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var posts, gets, nodeGets atomic.Int32
			r, p, s := kubeHarness(t, func(w http.ResponseWriter, q *http.Request) {
				base := "/v1/projects/" + kubeProject + "/kubernetes-clusters/" + kubeID
				switch {
				case q.Method == "POST" && q.URL.Path == base+"/upgrade":
					posts.Add(1)
					var payload map[string]any
					if err := json.NewDecoder(q.Body).Decode(&payload); err != nil || len(payload) != 1 || payload["k8s_version"] != "1.35.0" {
						t.Error("wrong upgrade payload", payload, err)
					}
					if tc.name == "ambiguous POST" {
						c, _, _ := w.(http.Hijacker).Hijack()
						_ = c.Close()
						return
					}
					if tc.name == "forbidden" || tc.name == "unsupported target" || tc.name == "conflict" {
						code := 403
						if tc.name == "unsupported target" {
							code = 422
						}
						if tc.name == "conflict" {
							code = 409
						}
						w.WriteHeader(code)
						_, _ = w.Write([]byte(`{"detail":"secret marker"}`))
						return
					}
					body := upgradeCluster("1.35.0", true, "active")
					switch tc.name {
					case "invalid acknowledgement":
						body = "{"
					case "foreign acknowledgement":
						body = strings.Replace(body, kubeID, kubeRegion, 1)
					case "wrong target acknowledgement":
						body = upgradeCluster("1.34.9", true, "active")
					case "invalid endpoint acknowledgement":
						body = strings.Replace(body, `"endpoint_port":6443`, `"endpoint_port":0`, 1)
					case "missing progress acknowledgement":
						body = strings.Replace(body, `,"upgrading":true`, "", 1)
					}
					_, _ = w.Write([]byte(body))
				case q.Method == "GET" && q.URL.Path == base:
					n := gets.Add(1)
					body := upgradeCluster("1.34.9", false, "active")
					if n == 1 {
						switch tc.name {
						case "resume rolling":
							body = upgradeCluster("1.35.0", true, "active")
						case "resume completed":
							body = upgradeCluster("1.35.0", false, "active")
						case "initial busy":
							body = upgradeCluster("1.34.9", true, "active")
						case "initial scaling":
							body = upgradeCluster("1.34.9", false, "scaling")
						case "initial missing progress":
							body = kubeBody
						case "initial drift":
							body = strings.Replace(body, "fixture-kubernetes", "other", 1)
						case "initial concurrent version":
							body = upgradeCluster("1.34.10", false, "active")
						case "initial missing":
							w.WriteHeader(404)
							return
						}
					} else {
						rolling := n == 2 && tc.name != "resume completed"
						body = upgradeCluster("1.35.0", rolling, "active")
						switch tc.name {
						case "timeout":
							body = upgradeCluster("1.35.0", true, "active")
						case "wait pools":
							if n == 3 {
								body = upgradeCluster("1.35.0", false, "provisioning")
							}
						case "poll forbidden":
							w.WriteHeader(403)
							return
						case "poll malformed":
							body = "{"
						case "poll drift":
							body = upgradeCluster("1.35.1", false, "active")
						case "poll missing progress":
							body = strings.Replace(body, `,"upgrading":true`, "", 1)
						case "control-plane replaced":
							body = strings.Replace(body, "10000000-0000-4000-8000-000000000005", kubeRegion, 1)
						case "pool replaced":
							body = strings.Replace(body, kubeWorkerPool, kubeRegion, 1)
						case "poll deleting":
							body = strings.Replace(body, `"status":"ready"`, `"status":"deleting"`, 1)
						case "pool error":
							body = upgradeCluster("1.35.0", true, "error")
						}
					}
					_, _ = w.Write([]byte(body))
				case q.Method == "GET" && q.URL.Path == base+"/nodes":
					if tc.name == "initial nodes forbidden" {
						w.WriteHeader(403)
						return
					}
					n := nodeGets.Add(1)
					nodes := scalingNodes(2)
					if tc.name == "wait nodes" && n == 2 {
						nodes = nodes[:2]
					}
					_ = json.NewEncoder(w).Encode(nodes)
				default:
					t.Error("unexpected mutation/request", q.Method, q.URL.Path)
					w.WriteHeader(500)
				}
			})
			r.operationTimeout = 500 * time.Millisecond
			if tc.name == "timeout" {
				r.operationTimeout = 20 * time.Millisecond
			}
			model := kubeState(t, s)
			model.K8sVersion = types.StringValue("1.35.0")
			if d := p.Set(context.Background(), &model); d.HasError() {
				t.Fatal(d)
			}
			resp := resource.UpdateResponse{State: s}
			r.Update(context.Background(), resource.UpdateRequest{State: s, Plan: p}, &resp)
			if resp.Diagnostics.HasError() != tc.fail {
				t.Fatal(resp.Diagnostics)
			}
			if posts.Load() != int32(tc.posts) {
				t.Fatal("unexpected POST count", posts.Load())
			}
			m := kubeState(t, resp.State)
			if m.ID.ValueString() != kubeID || m.ProjectID.ValueString() != kubeProject || m.WorkerCount.ValueInt64() != 2 || m.TalosVersion.ValueString() != "1.12.4" {
				t.Fatal("identity or other inputs changed")
			}
			if !tc.fail && m.K8sVersion.ValueString() != "1.35.0" {
				t.Fatal("target lost")
			}
			if tc.name == "timeout" || tc.name == "poll forbidden" {
				if m.K8sVersion.ValueString() != "1.35.0" {
					t.Fatal("acknowledged target lost")
				}
			}
			if tc.name == "success" && gets.Load() < 3 {
				t.Fatal("returned while upgrading")
			}
			for _, d := range resp.Diagnostics {
				if strings.Contains(d.Detail(), "secret marker") {
					t.Fatal("body leaked")
				}
			}
		})
	}
}
func TestKubernetesUpgradePlanningGuards(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, target  string
		workers       int64
		replace, fail bool
	}{
		{"patch", "1.34.10", 2, false, false}, {"minor", "1.35.0", 2, false, false}, {"older patch", "1.34.8", 2, false, true},
		{"older minor", "1.33.13", 2, false, true}, {"skip minor", "1.36.0", 2, false, true}, {"major", "2.0.0", 2, false, true},
		{"combined scaling", "1.35.0", 3, false, true}, {"replacement can use older version", "1.33.13", 2, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, p, s := kubeHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("plan called API") })
			m := kubeState(t, s)
			m.K8sVersion = types.StringValue(tc.target)
			m.WorkerCount = types.Int64Value(tc.workers)
			if tc.replace {
				m.Name = types.StringValue("new-cluster")
			}
			if d := p.Set(ctx, &m); d.HasError() {
				t.Fatal(d)
			}
			prior, _ := tfprotov6.NewDynamicValue(s.Raw.Type(), s.Raw)
			proposed, _ := tfprotov6.NewDynamicValue(p.Raw.Type(), p.Raw)
			result, err := providerserver.NewProtocol6(New())().PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: "tatnet_kubernetes_cluster", PriorState: &prior, ProposedNewState: &proposed, Config: &proposed})
			if err != nil {
				t.Fatal(err)
			}
			fail := false
			for _, d := range result.Diagnostics {
				fail = fail || d.Severity == tfprotov6.DiagnosticSeverityError
			}
			if fail != tc.fail || (!fail && (len(result.RequiresReplace) > 0) != tc.replace) {
				t.Fatal("upgrade plan policy", result.Diagnostics, result.RequiresReplace)
			}
		})
	}
	for _, target := range []string{"1.34.8", "1.36.0", "2.0.0", "1.34.9999999999999999999"} {
		r, p, s := kubeHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid upgrade reached API") })
		m := kubeState(t, s)
		m.K8sVersion = types.StringValue(target)
		_ = p.Set(ctx, &m)
		resp := resource.UpdateResponse{State: s}
		r.Update(ctx, resource.UpdateRequest{Plan: p, State: s}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("unsafe upgrade accepted")
		}
	}
}
