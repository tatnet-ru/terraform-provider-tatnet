package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

const catalogue = `{"images":[{"id":"regional-build","name":"RED OS","filename":"redos.qcow2"}],"families":[{"slug":"redos","name":"RED OS","kind":"os","os_family":"redos","versions":[{"version":"7.3","image_id":"global-newest","by_cluster":{"region-a":"regional-build"}}]}]}`

func TestImageRead(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		wantError  string
	}{
		{"regional build", catalogue, 200, ""},
		{"grant absent", `{"images":[],"families":[]}`, 200, "no accessible OS image"},
		{"different region", strings.ReplaceAll(catalogue, "region-a", "region-b"), 200, "no accessible OS image"},
		{"missing regional map", strings.ReplaceAll(catalogue, `"by_cluster":{"region-a":"regional-build"}`, `"by_cluster":{}`), 200, "no accessible OS image"},
		{"unlisted build", strings.Replace(catalogue, `"id":"regional-build"`, `"id":"another-build"`, 1), 200, "no accessible OS image"},
		{"internal family", strings.ReplaceAll(catalogue, `"kind":"os"`, `"kind":"service_node"`), 200, "no accessible OS image"},
		{"wrong version", strings.ReplaceAll(catalogue, "7.3", "8"), 200, "no accessible OS image"},
		{"forbidden", `{"detail":"secret response"}`, 403, "HTTP 403"},
		{"unauthorized", `{}`, 401, "HTTP 401"},
		{"missing project", `{}`, 404, "HTTP 404"},
		{"server failure", `{}`, 500, "HTTP 500"},
		{"invalid JSON", `{`, 200, "request failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/projects/project-a/images" || r.URL.Query().Get("cluster_id") != "region-a" || r.URL.Query().Has("account_id") {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("missing key")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := tatnet.NewClientWithResponses(server.URL+"/v1", tatnet.WithAPIKey("test-key"))
			if err != nil {
				t.Fatal(err)
			}
			d := &imageDataSource{client: client}
			ctx := context.Background()
			var sr datasource.SchemaResponse
			d.Schema(ctx, datasource.SchemaRequest{}, &sr)
			raw := tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
				"project_id": tftypes.NewValue(tftypes.String, "project-a"), "cluster_id": tftypes.NewValue(tftypes.String, "region-a"),
				"family": tftypes.NewValue(tftypes.String, "redos"), "version": tftypes.NewValue(tftypes.String, "7.3"), "id": tftypes.NewValue(tftypes.String, nil),
			})
			req := datasource.ReadRequest{Config: tfsdk.Config{Raw: raw, Schema: sr.Schema}}
			resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema}}
			d.Read(ctx, req, &resp)
			if tc.wantError != "" {
				if !resp.Diagnostics.HasError() {
					t.Fatal("expected diagnostic")
				}
				detail := resp.Diagnostics.Errors()[0].Detail()
				if !strings.Contains(detail, tc.wantError) || strings.Contains(detail, "secret response") {
					t.Fatalf("unexpected diagnostic: %s", detail)
				}
				return
			}
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			var state imageModel
			if diags := resp.State.Get(ctx, &state); diags.HasError() {
				t.Fatal(diags)
			}
			if state.ID.ValueString() != "regional-build" || state.ProjectID.ValueString() != "project-a" {
				t.Fatalf("unexpected state: %+v", state)
			}
		})
	}
}
