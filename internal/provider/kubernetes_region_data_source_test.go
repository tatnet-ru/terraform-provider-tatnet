package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

const kubeRegions = `{"regions":[{"id":"` + kubeRegion + `","name":"fixture-region","is_default":true,"talos_versions":["v1.12.4"],"default_talos_version":"v1.12.4"}],"k8s_versions":["1.34.9","1.33.13"],"default_k8s_version":"1.34.9","talos_versions":["9.9.9"],"default_talos_version":"9.9.9"}`

func TestKubernetesRegionRead(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
		fail       bool
	}{
		{"regional versions", kubeRegions, 200, false}, {"missing region", strings.Replace(kubeRegions, kubeRegion, kubeID, 1), 200, true}, {"missing default", strings.Replace(kubeRegions, `"default_k8s_version":"1.34.9"`, `"default_k8s_version":"1.35.0"`, 1), 200, true}, {"bad versions", strings.Replace(kubeRegions, "v1.12.4", "latest", -1), 200, true}, {"empty", `{}`, 200, true}, {"null", `null`, 200, true}, {"bad JSON", `{`, 200, true}, {"unauthorized", `{"detail":"secret marker"}`, 401, true}, {"forbidden", `{"detail":"secret marker"}`, 403, true}, {"server error", `{}`, 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				requests++
				if q.Method != "GET" || q.URL.Path != "/v1/kubernetes/regions" || q.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("unexpected discovery request")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			c, err := tatnet.NewClientWithResponses(s.URL+"/v1", tatnet.WithAPIKey("test-key"))
			if err != nil {
				t.Fatal(err)
			}
			d := &kubernetesRegionDataSource{client: c}
			var sr datasource.SchemaResponse
			d.Schema(context.Background(), datasource.SchemaRequest{}, &sr)
			m := kubernetesRegionModel{ID: types.StringValue(kubeRegion), K8sVersions: types.ListNull(types.StringType), TalosVersions: types.ListNull(types.StringType)}
			state := tfsdk.State{Schema: sr.Schema}
			if diags := state.Set(context.Background(), &m); diags.HasError() {
				t.Fatal(diags)
			}
			cfg := tfsdk.Config{Schema: sr.Schema, Raw: state.Raw}
			resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema}}
			d.Read(context.Background(), datasource.ReadRequest{Config: cfg}, &resp)
			if resp.Diagnostics.HasError() != tc.fail {
				t.Fatal(resp.Diagnostics)
			}
			if requests != 1 {
				t.Fatal("discovery retried")
			}
			for _, diag := range resp.Diagnostics {
				if strings.Contains(diag.Detail(), "secret marker") {
					t.Fatal("API response leaked")
				}
			}
			if !tc.fail {
				var got kubernetesRegionModel
				if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
					t.Fatal(diags)
				}
				if got.DefaultTalosVersion.ValueString() != "v1.12.4" || got.Name.ValueString() != "fixture-region" {
					t.Fatal("selected global rather than regional default")
				}
			}
		})
	}
}
