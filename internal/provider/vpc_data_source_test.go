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

const testVPCID = "11111111-1111-4111-8111-111111111111"
const testRegionID = "22222222-2222-4222-8222-222222222222"
const vpcBody = `{"id":"11111111-1111-4111-8111-111111111111","cluster_id":"22222222-2222-4222-8222-222222222222","name":"fixture-network","subnet":"10.42.0.0/24","status":"active","is_default":false}`

func vpcRead(t *testing.T, d *vpcDataSource, id interface{}) datasource.ReadResponse {
	t.Helper()
	ctx := context.Background()
	var sr datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sr)
	values := map[string]tftypes.Value{}
	for _, k := range []string{"id", "cluster_id", "name", "subnet", "status"} {
		values[k] = tftypes.NewValue(tftypes.String, nil)
	}
	values["id"] = tftypes.NewValue(tftypes.String, id)
	values["is_default"] = tftypes.NewValue(tftypes.Bool, nil)
	config := tfsdk.Config{Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), values), Schema: sr.Schema}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema}}
	d.Read(ctx, datasource.ReadRequest{Config: config}, &resp)
	return resp
}
func TestVPCRead(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		wantError  string
	}{
		{"complete", vpcBody, 200, ""},
		{"optional omitted", strings.ReplaceAll(strings.ReplaceAll(vpcBody, `,"status":"active"`, ""), `,"is_default":false`, ""), 200, ""},
		{"wrong id", strings.Replace(vpcBody, testVPCID, testRegionID, 1), 200, "invalid response"},
		{"empty response", `{}`, 200, "invalid response"},
		{"null response", `null`, 200, "invalid response"},
		{"invalid region", strings.Replace(vpcBody, testRegionID, "wrong", 1), 200, "invalid response"},
		{"bad cidr", strings.Replace(vpcBody, "10.42.0.0/24", "bad", 1), 200, "invalid IPv4 subnet"},
		{"host bits", strings.Replace(vpcBody, "10.42.0.0/24", "10.42.0.1/24", 1), 200, "invalid IPv4 subnet"},
		{"ipv6", strings.Replace(vpcBody, "10.42.0.0/24", "fd00::/64", 1), 200, "invalid IPv4 subnet"},
		{"unauthorized", `{"detail":"secret marker"}`, 401, "HTTP 401"},
		{"forbidden", `{"detail":"secret marker"}`, 403, "HTTP 403"},
		{"absent", `{}`, 404, "HTTP 404"},
		{"server failure", `{"detail":"secret marker"}`, 500, "HTTP 500"},
		{"malformed json", `{`, 200, "request failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != "GET" || r.URL.Path != "/v1/vpcs/"+testVPCID || r.URL.RawQuery != "" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("missing auth")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			client, err := tatnet.NewClientWithResponses(s.URL+"/v1", tatnet.WithAPIKey("test-key"))
			if err != nil {
				t.Fatal(err)
			}
			resp := vpcRead(t, &vpcDataSource{client: client}, testVPCID)
			if requests != 1 {
				t.Fatalf("unexpected request count %d", requests)
			}
			if tc.wantError != "" {
				if !resp.Diagnostics.HasError() {
					t.Fatal("expected diagnostic")
				}
				detail := resp.Diagnostics.Errors()[0].Detail()
				if !strings.Contains(detail, tc.wantError) || strings.Contains(detail, "secret marker") {
					t.Fatalf("unexpected diagnostic %s", detail)
				}
				return
			}
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			var m vpcModel
			if ds := resp.State.Get(context.Background(), &m); ds.HasError() {
				t.Fatal(ds)
			}
			if m.ID.ValueString() != testVPCID || m.ClusterID.ValueString() != testRegionID || m.Subnet.ValueString() != "10.42.0.0/24" {
				t.Fatalf("wrong state %+v", m)
			}
			if tc.name == "optional omitted" && (!m.Status.IsNull() || !m.IsDefault.IsNull()) {
				t.Fatal("omitted fields must remain null")
			}
		})
	}
}
func TestVPCInvalidSelector(t *testing.T) {
	for _, id := range []interface{}{nil, tftypes.UnknownValue, "", "../images", "bad-id"} {
		if r := vpcRead(t, &vpcDataSource{}, id); !r.Diagnostics.HasError() || r.Diagnostics.Errors()[0].Summary() != "Invalid VPC ID" {
			t.Fatalf("unexpected diagnostics for %v", id)
		}
	}
	if r := vpcRead(t, &vpcDataSource{}, testVPCID); !r.Diagnostics.HasError() || r.Diagnostics.Errors()[0].Summary() != "Provider not configured" {
		t.Fatal("expected missing client diagnostic")
	}
}
