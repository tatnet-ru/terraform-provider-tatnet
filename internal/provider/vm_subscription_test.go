package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

func TestVMPaymentPeriods(t *testing.T) {
	for _, tc := range []struct {
		days, months   int64
		explicit, want string
		invalid        bool
	}{
		{1, 0, "", "1d", false}, {0, 1, "", "1m", false}, {0, 3, "", "3m", false},
		{0, 6, "", "6m", false}, {0, 12, "", "12m", false}, {0, 0, "1h", "1h", false},
		{0, 0, "1m", "1m", false}, {30, 0, "", "", true}, {7, 0, "", "", true},
		{1, 1, "", "", true}, {0, 24, "", "", true}, {0, 2, "", "", true},
		{0, 0, "", "", true}, {1, 0, "1m", "", true}, {0, 0, "30d", "", true},
	} {
		t.Run(fmt.Sprintf("%d/%d/%s", tc.days, tc.months, tc.explicit), func(t *testing.T) {
			m := testVMModel(t)
			m.PeriodDays = types.Int64Null()
			if tc.days != 0 {
				m.PeriodDays = types.Int64Value(tc.days)
			}
			if tc.months != 0 {
				m.PeriodMonths = types.Int64Value(tc.months)
			}
			if tc.explicit != "" {
				m.BillingPeriod = types.StringValue(tc.explicit)
			}
			got, err := m.paymentPeriod()
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatalf("period = %q, %v", got, err)
			}
		})
	}
}

func TestVMSubscriptionPlanNeverReplacesVM(t *testing.T) {
	ctx := context.Background()
	for _, legacy := range []int64{1, 30} {
		_, plan, state := vmHarness(t, func(http.ResponseWriter, *http.Request) { t.Fatal("planning called API") })
		var before vmModel
		state.Get(ctx, &before)
		before.PeriodDays = types.Int64Value(legacy)
		state.Set(ctx, &before)
		var after vmModel
		plan.Get(ctx, &after)
		after.PeriodDays, after.PeriodMonths = types.Int64Null(), types.Int64Value(1)
		after.AutoRenew = types.BoolValue(true)
		plan.Set(ctx, &after)
		typ := plan.Raw.Type()
		prior, _ := tfprotov6.NewDynamicValue(typ, state.Raw)
		proposed, _ := tfprotov6.NewDynamicValue(typ, plan.Raw)
		result, err := providerserver.NewProtocol6(New())().PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{TypeName: "tatnet_vm", PriorState: &prior, ProposedNewState: &proposed, Config: &proposed})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range result.Diagnostics {
			if d.Severity == tfprotov6.DiagnosticSeverityError {
				t.Fatal(d.Detail)
			}
		}
		if len(result.RequiresReplace) != 0 {
			t.Fatalf("billing migration requires replacement: %+v", result.RequiresReplace)
		}
	}
}

func TestVMSubscriptionUpdateRetainsIdentity(t *testing.T) {
	for _, code := range []int{200, 402, 403, 409, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			ctx := context.Background()
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "PATCH" || r.URL.Path != "/v1/projects/project-a/vms/vm-a/subscription" {
					t.Errorf("unexpected VM mutation: %s %s", r.Method, r.URL)
				}
				var body tatnet.V1VMSubscriptionUpdate
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.PeriodType == nil || *body.PeriodType != tatnet.N1m || body.AutoRenew == nil || !*body.AutoRenew {
					t.Errorf("wrong billing patch: %+v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				if code == 200 {
					_, _ = w.Write([]byte(`{"resource_id":"vm-a","period_type":"1m","amount_per_period":"3474.00","currency":"RUB","status":"active","auto_renew":true}`))
				}
			}))
			defer srv.Close()
			r, plan, state := vmHarness(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") })
			client, err := tatnet.NewClientWithResponses(srv.URL+"/v1", tatnet.WithAPIKey("test-key"))
			if err != nil {
				t.Fatal(err)
			}
			r.client = client
			var desired vmModel
			plan.Get(ctx, &desired)
			desired.PeriodDays, desired.PeriodMonths = types.Int64Null(), types.Int64Value(1)
			desired.AutoRenew = types.BoolValue(true)
			plan.Set(ctx, &desired)
			resp := resource.UpdateResponse{State: state}
			r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, &resp)
			if resp.Diagnostics.HasError() != (code != 200) {
				t.Fatal(resp.Diagnostics)
			}
			var got vmModel
			resp.State.Get(ctx, &got)
			if got.ID.ValueString() != "vm-a" || calls != 1 {
				t.Fatalf("lost identity or retried: %s, %d", got.ID, calls)
			}
			if code == 200 && (got.SubscriptionPeriod.ValueString() != "1m" || got.AmountPerPeriod.ValueString() != "3474.00" || got.Status.ValueString() != "running") {
				t.Fatalf("incorrect state: %+v", got)
			}
		})
	}
}

func TestVMMonthlyAndHourlyCreatePayload(t *testing.T) {
	for _, interval := range []string{"1m", "3m", "1h"} {
		t.Run(interval, func(t *testing.T) {
			ctx := context.Background()
			r, plan, _ := vmHarness(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") })
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if q.Method == "POST" {
					calls++
					var body tatnet.V1VMCreate
					json.NewDecoder(q.Body).Decode(&body)
					if body.PeriodDays != nil {
						t.Error("daily field sent for non-daily subscription")
					}
					if interval == "1h" && body.PeriodMonths != nil {
						t.Error("months sent for hourly subscription")
					}
					if interval != "1h" && (body.PeriodMonths == nil || fmt.Sprintf("%dm", *body.PeriodMonths) != interval) {
						t.Errorf("incorrect months: %+v", body)
					}
					w.WriteHeader(201)
					w.Write([]byte(vmJSON("active")))
					return
				}
				if q.Method != "GET" {
					t.Errorf("unexpected request: %s", q.Method)
				}
				fmt.Fprintf(w, `{"resource_id":"vm-a","period_type":%q,"amount_per_period":"3474.00","currency":"RUB","status":"active","auto_renew":false}`, interval)
			}))
			defer srv.Close()
			r.client, _ = tatnet.NewClientWithResponses(srv.URL+"/v1", tatnet.WithAPIKey("test-key"))
			r.operationTimeout = time.Second
			var m vmModel
			plan.Get(ctx, &m)
			m.PeriodDays = types.Int64Null()
			m.BillingPeriod = types.StringValue(interval)
			plan.Set(ctx, &m)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			if resp.Diagnostics.HasError() || calls != 1 {
				t.Fatalf("create: %v, calls=%d", resp.Diagnostics, calls)
			}
		})
	}
}

func TestVMSubscriptionRefreshDriftAndFailures(t *testing.T) {
	for _, code := range []int{200, 403, 404, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			ctx := context.Background()
			r, _, state := vmHarness(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") })
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" {
					t.Errorf("refresh mutated VM: %s", q.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				if q.URL.Path == "/v1/projects/project-a/vms/vm-a" {
					w.Write([]byte(vmJSON("active")))
					return
				}
				if q.URL.Path != "/v1/projects/project-a/vms/vm-a/subscription" {
					t.Errorf("wrong path: %s", q.URL)
				}
				w.WriteHeader(code)
				if code == 200 {
					w.Write([]byte(`{"resource_id":"vm-a","period_type":"3m","amount_per_period":"9900.00","currency":"RUB","status":"active","auto_renew":true}`))
				}
			}))
			defer srv.Close()
			r.client, _ = tatnet.NewClientWithResponses(srv.URL+"/v1", tatnet.WithAPIKey("test-key"))
			resp := resource.ReadResponse{State: state}
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != (code != 200) {
				t.Fatal(resp.Diagnostics)
			}
			var m vmModel
			resp.State.Get(ctx, &m)
			if m.ID.ValueString() != "vm-a" || resp.State.Raw.IsNull() {
				t.Fatal("billing error removed the VM from state")
			}
			if code == 200 && (m.SubscriptionPeriod.ValueString() != "3m" || !m.PeriodDays.IsNull() || !m.AutoRenew.ValueBool() || m.AmountPerPeriod.ValueString() != "9900.00") {
				t.Fatalf("billing drift not refreshed: %+v", m)
			}
		})
	}
}

func TestVMSubscriptionUpdateRejectsHardwareChange(t *testing.T) {
	ctx := context.Background()
	r, plan, state := vmHarness(t, func(http.ResponseWriter, *http.Request) { t.Fatal("invalid update reached the API") })
	var m vmModel
	plan.Get(ctx, &m)
	m.ImageID = types.StringValue("new-image")
	plan.Set(ctx, &m)
	resp := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("hardware update accepted by billing-only Update")
	}
}
