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

func talosCluster(pin, observed string, rolling bool) string {
	var v tatnet.V1K8sCluster
	_ = json.Unmarshal([]byte(upgradeCluster("1.34.9", rolling, "active")), &v)
	v.TalosVersion = pin
	if observed != "" {
		v.NodeTalosVersion = &observed
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func TestTalosVersionUpgrade(t *testing.T) {
	cases := []struct {
		name  string
		fail  bool
		posts int
	}{
		{"success", false, 1}, {"resume rolling", false, 0}, {"resume completed", false, 0}, {"wait observation", false, 1}, {"timeout", true, 1},
		{"ambiguous POST", true, 1}, {"forbidden", true, 1}, {"unsupported", true, 1}, {"wrong target ack", true, 1}, {"wrong Kubernetes ack", true, 1},
		{"initial missing observation", true, 0}, {"initial observed newer", true, 0}, {"initial malformed observation", true, 0}, {"initial busy", true, 0},
		{"poll missing progress", true, 1}, {"poll malformed", true, 1}, {"poll observed newer", true, 1}, {"poll pool replaced", true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gets, posts atomic.Int32
			r, p, s := kubeHarness(t, func(w http.ResponseWriter, q *http.Request) {
				base := "/v1/projects/" + kubeProject + "/kubernetes-clusters/" + kubeID
				switch {
				case q.Method == "POST" && q.URL.Path == base+"/upgrade":
					posts.Add(1)
					var payload map[string]any
					if e := json.NewDecoder(q.Body).Decode(&payload); e != nil || len(payload) != 1 || payload["talos_version"] != "v1.13.5" {
						t.Error("wrong OS upgrade payload", payload, e)
					}
					if tc.name == "ambiguous POST" {
						c, _, _ := w.(http.Hijacker).Hijack()
						_ = c.Close()
						return
					}
					if tc.name == "forbidden" || tc.name == "unsupported" {
						code := 403
						if tc.name == "unsupported" {
							code = 422
						}
						w.WriteHeader(code)
						_, _ = w.Write([]byte(`{"detail":"secret marker"}`))
						return
					}
					body := talosCluster("v1.13.5", "v1.13.4", true)
					if tc.name == "wrong target ack" {
						body = talosCluster("v1.13.4", "v1.13.4", true)
					}
					if tc.name == "wrong Kubernetes ack" {
						body = strings.Replace(body, `"k8s_version":"1.34.9"`, `"k8s_version":"1.35.0"`, 1)
					}
					_, _ = w.Write([]byte(body))
				case q.Method == "GET" && q.URL.Path == base:
					n := gets.Add(1)
					body := talosCluster("v1.13.4", "v1.13.4", false)
					if n == 1 {
						switch tc.name {
						case "resume rolling":
							body = talosCluster("v1.13.5", "v1.13.5", true)
						case "resume completed":
							body = talosCluster("v1.13.5", "v1.13.5", false)
						case "initial missing observation":
							body = talosCluster("v1.13.4", "", false)
						case "initial observed newer":
							body = talosCluster("v1.13.4", "v1.13.6", false)
						case "initial malformed observation":
							body = talosCluster("v1.13.4", "v1.10.9999999999999999999", false)
						case "initial busy":
							body = talosCluster("v1.13.4", "v1.13.4", true)
						}
					} else {
						rolling := n == 2 && tc.name != "resume completed"
						observed := "v1.13.5"
						if rolling {
							observed = "v1.13.4"
						}
						body = talosCluster("v1.13.5", observed, rolling)
						switch tc.name {
						case "wait observation":
							if n == 3 {
								body = talosCluster("v1.13.5", "v1.13.4", false)
							}
						case "timeout":
							body = talosCluster("v1.13.5", "v1.13.4", true)
						case "poll missing progress":
							body = strings.Replace(body, `,"upgrading":true`, "", 1)
						case "poll malformed":
							body = "{"
						case "poll observed newer":
							body = talosCluster("v1.13.5", "v1.13.6", false)
						case "poll pool replaced":
							body = strings.Replace(body, kubeWorkerPool, kubeRegion, 1)
						}
					}
					_, _ = w.Write([]byte(body))
				case q.Method == "GET" && q.URL.Path == base+"/nodes":
					_ = json.NewEncoder(w).Encode(scalingNodes(2))
				default:
					t.Error("unexpected request", q.Method, q.URL.Path)
					w.WriteHeader(500)
				}
			})
			r.operationTimeout = 500 * time.Millisecond
			if tc.name == "timeout" {
				r.operationTimeout = 20 * time.Millisecond
			}
			m := kubeState(t, s)
			m.TalosVersion = types.StringValue("v1.13.4")
			if d := s.Set(context.Background(), &m); d.HasError() {
				t.Fatal(d)
			}
			m.TalosVersion = types.StringValue("v1.13.5")
			if d := p.Set(context.Background(), &m); d.HasError() {
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
			after := kubeState(t, resp.State)
			if after.ID.ValueString() != kubeID || after.K8sVersion.ValueString() != "1.34.9" || after.WorkerCount.ValueInt64() != 2 {
				t.Fatal("identity/Kubernetes/topology changed")
			}
			if (!tc.fail || tc.name == "timeout") && after.TalosVersion.ValueString() != "v1.13.5" {
				t.Fatal("OS target lost")
			}
			if tc.name == "success" && gets.Load() < 3 {
				t.Fatal("returned while rebooting")
			}
			for _, d := range resp.Diagnostics {
				if strings.Contains(d.Detail(), "secret marker") {
					t.Fatal("response leaked")
				}
			}
		})
	}
}
func TestTalosUpgradePlanGuards(t *testing.T) {
	cases := []struct {
		name, target, kube string
		workers            int64
		fail               bool
	}{
		{"patch", "v1.13.5", "1.34.9", 2, false}, {"minor", "v1.14.0", "1.34.9", 2, false}, {"downgrade", "v1.13.3", "1.34.9", 2, true}, {"skip minor", "v1.15.0", "1.34.9", 2, true}, {"major", "v2.0.0", "1.34.9", 2, true}, {"missing prefix", "1.13.5", "1.34.9", 2, true}, {"combined Kubernetes", "v1.13.5", "1.35.0", 2, true}, {"combined scaling", "v1.13.5", "1.34.9", 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, p, s := kubeHarness(t, func(http.ResponseWriter, *http.Request) { t.Error("plan contacted API") })
			m := kubeState(t, s)
			m.TalosVersion = types.StringValue("v1.13.4")
			_ = s.Set(ctx, &m)
			m.TalosVersion = types.StringValue(tc.target)
			m.K8sVersion = types.StringValue(tc.kube)
			m.WorkerCount = types.Int64Value(tc.workers)
			_ = p.Set(ctx, &m)
			prior, _ := tfprotov6.NewDynamicValue(s.Raw.Type(), s.Raw)
			proposed, _ := tfprotov6.NewDynamicValue(p.Raw.Type(), p.Raw)
			result, e := providerserver.NewProtocol6(New())().PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: "tatnet_kubernetes_cluster", PriorState: &prior, ProposedNewState: &proposed, Config: &proposed})
			if e != nil {
				t.Fatal(e)
			}
			fail := false
			for _, d := range result.Diagnostics {
				fail = fail || d.Severity == tfprotov6.DiagnosticSeverityError
			}
			if fail != tc.fail || (!fail && len(result.RequiresReplace) != 0) {
				t.Fatal("OS plan policy", result.Diagnostics, result.RequiresReplace)
			}
		})
	}
}
