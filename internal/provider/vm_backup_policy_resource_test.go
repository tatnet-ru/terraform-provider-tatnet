package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

func policyState(t *testing.T, m vmBackupPolicyModel) tfsdk.State {
	t.Helper()
	var sr resource.SchemaResponse
	NewVMBackupPolicyResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	s := tfsdk.State{Schema: sr.Schema}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return s
}
func policyFixture() vmBackupPolicyModel {
	return vmBackupPolicyModel{ProjectID: types.StringValue(testVPCID), VMID: types.StringValue(testRegionID), ID: types.StringValue(testVPCID + "/" + testRegionID), Enabled: types.BoolValue(true), HourUTC: types.Int64Value(5), RetentionDays: types.Int64Value(7), NextRunAt: types.StringNull(), StorageBytes: types.Int64Value(0), PricePerGBHour: types.StringValue("0.01")}
}
func TestBackupPolicyLifecycle(t *testing.T) {
	ctx := context.Background()
	enabled, hour, days := false, 2, 3
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		if req.URL.Path != "/v1/projects/"+testVPCID+"/vms/"+testRegionID+"/backup-policy" || req.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("wrong authenticated policy route")
		}
		if req.Method == "PUT" {
			var body tatnet.VMBackupPolicyRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			// Match API request defaults: PUT is a complete policy update.
			hour, days = 0, 7
			if body.Enabled != nil {
				enabled = *body.Enabled
			}
			if body.HourUtc != nil {
				hour = *body.HourUtc
			}
			if body.RetentionDays != nil {
				days = *body.RetentionDays
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"enabled": enabled, "hour_utc": hour, "retention_days": days, "storage_bytes": 123, "price_per_gb_hour": "0.01"})
	}))
	defer server.Close()
	client, err := tatnet.NewClientWithResponses(server.URL+"/v1", tatnet.WithAPIKey("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	r := &vmBackupPolicyResource{client: client}
	m := policyFixture()
	state := policyState(t, m)
	create := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}}, &create)
	if create.Diagnostics.HasError() {
		t.Fatal(create.Diagnostics)
	}
	if !enabled || hour != 5 || days != 7 {
		t.Fatal("settings not written")
	}
	m.RetentionDays = types.Int64Value(30)
	state = policyState(t, m)
	update := resource.UpdateResponse{State: create.State}
	r.Update(ctx, resource.UpdateRequest{State: create.State, Plan: tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}}, &update)
	if update.Diagnostics.HasError() {
		t.Fatal(update.Diagnostics)
	}
	// An external schedule edit must survive Terraform destroy; stored data is never deleted.
	hour = 9
	read := resource.ReadResponse{State: update.State}
	r.Read(ctx, resource.ReadRequest{State: update.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	var observed vmBackupPolicyModel
	if d := read.State.Get(ctx, &observed); d.HasError() {
		t.Fatal(d)
	}
	if observed.HourUTC.ValueInt64() != 9 || observed.StorageBytes.ValueInt64() != 123 {
		t.Fatal("drift/usage not refreshed")
	}
	hour = 11 // A newer external edit after the last Terraform refresh.
	deletion := resource.DeleteResponse{State: read.State}
	r.Delete(ctx, resource.DeleteRequest{State: read.State}, &deletion)
	if deletion.Diagnostics.HasError() {
		t.Fatal(deletion.Diagnostics)
	}
	if enabled || hour != 11 || days != 30 || requests != 5 {
		t.Fatal("destroy changed more than enabled")
	}
}
func TestBackupPolicyReadFailureRetainsState(t *testing.T) {
	for _, status := range []int{403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); w.Write([]byte("secret marker")) }))
			defer s.Close()
			c, _ := tatnet.NewClientWithResponses(s.URL + "/v1")
			r := &vmBackupPolicyResource{client: c}
			state := policyState(t, policyFixture())
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if !resp.Diagnostics.HasError() || strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "secret marker") || !resp.State.Raw.Equal(state.Raw) {
				t.Fatal("failure leaked response or lost state")
			}
		})
	}
}
func TestBackupPolicyMissingVMRemovesState(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer s.Close()
	c, _ := tatnet.NewClientWithResponses(s.URL + "/v1")
	r := &vmBackupPolicyResource{client: c}
	state := policyState(t, policyFixture())
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Fatal("missing VM must remove policy state")
	}
}
