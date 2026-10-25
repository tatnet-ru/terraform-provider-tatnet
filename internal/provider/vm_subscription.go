package provider

// Subscription settings use the billing API exclusively. Keep financial
// updates separate from VM creation, power, resize and deletion operations.
import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

// creationPeriod translates the chosen interval into the VM creation API's
// mutually exclusive fields. A calendar month never becomes a count of days.
func (m *vmModel) creationPeriod() (days, months *int, err error) {
	period, err := m.paymentPeriod()
	if err != nil {
		return nil, nil, err
	}
	if period == "1d" {
		v := 1
		return &v, nil, nil
	}
	if strings.HasSuffix(period, "m") {
		v, _ := strconv.Atoi(strings.TrimSuffix(period, "m"))
		return nil, &v, nil
	}
	return nil, nil, nil
}

func (r *vmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var desired, prior vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &desired)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !desired.ProjectID.Equal(prior.ProjectID) || !desired.ClusterID.Equal(prior.ClusterID) ||
		!desired.ImageID.Equal(prior.ImageID) || !desired.PlanID.Equal(prior.PlanID) ||
		!desired.Name.Equal(prior.Name) || !desired.Hostname.Equal(prior.Hostname) ||
		!desired.DefaultUser.Equal(prior.DefaultUser) || !desired.VPCID.Equal(prior.VPCID) || !desired.SSHKeys.Equal(prior.SSHKeys) {
		resp.Diagnostics.AddError("VM replacement required", "Only subscription settings can change in place.")
		return
	}
	period, err := desired.paymentPeriod()
	if err != nil {
		resp.Diagnostics.AddError("Invalid payment period", err.Error())
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure TatNet before updating a subscription.")
		return
	}
	desired.ID = prior.ID
	p := tatnet.V1VMSubscriptionUpdatePeriodType(period)
	renew := desired.AutoRenew.ValueBool()
	result, err := r.client.VmsPatchVmSubscriptionWithResponse(ctx, desired.ProjectID.ValueString(), desired.ID.ValueString(), tatnet.V1VMSubscriptionUpdate{PeriodType: &p, AutoRenew: &renew})
	if err != nil {
		resp.Diagnostics.AddError("VM subscription outcome unknown", "Refresh the subscription before retrying; the VM identity is retained.")
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError("Cannot update VM subscription", fmt.Sprintf("API returned HTTP %d. Check billing permission, balance and paid period. The VM was not replaced.", result.StatusCode()))
		return
	}
	if result.JSON200.ResourceId != prior.ID.ValueString() || result.JSON200.PeriodType != period || result.JSON200.AutoRenew != renew {
		resp.Diagnostics.AddError("VM subscription not confirmed", "API returned different subscription settings; refresh before retrying.")
		return
	}
	desired.observeSubscription(result.JSON200)
	// Subscription updates do not change the VM's observed power/network state.
	desired.Status, desired.InterfaceID, desired.IPv4 = prior.Status, prior.InterfaceID, prior.IPv4
	resp.Diagnostics.Append(resp.State.Set(ctx, &desired)...)
}

func (m *vmModel) paymentPeriod() (string, error) {
	count := 0
	period := ""
	if !m.PeriodDays.IsNull() {
		count++
		if m.PeriodDays.IsUnknown() || m.PeriodDays.ValueInt64() != 1 {
			return "", fmt.Errorf("period_days supports only 1; replace period_days=30 with period_months=1")
		}
		period = "1d"
	}
	if !m.PeriodMonths.IsNull() {
		count++
		if m.PeriodMonths.IsUnknown() {
			return "", fmt.Errorf("period_months must be known")
		}
		period = fmt.Sprintf("%dm", m.PeriodMonths.ValueInt64())
	}
	if !m.BillingPeriod.IsNull() {
		count++
		if m.BillingPeriod.IsUnknown() {
			return "", fmt.Errorf("billing_period must be known")
		}
		period = m.BillingPeriod.ValueString()
	}
	if count != 1 {
		return "", fmt.Errorf("specify exactly one of billing_period, period_months, period_days")
	}
	if tatnet.V1VMSubscriptionUpdatePeriodType(period).Valid() {
		return period, nil
	}
	return "", fmt.Errorf("supported periods: 1h, 1d, 1m, 3m, 6m, 12m")
}

func (r *vmResource) readSubscription(ctx context.Context, m *vmModel) error {
	result, err := r.client.VmsGetVmSubscriptionWithResponse(ctx, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		return fmt.Errorf("subscription read failed; VM state retained")
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		return fmt.Errorf("subscription read returned HTTP %d; VM state retained", result.StatusCode())
	}
	if result.JSON200.ResourceId != m.ID.ValueString() {
		return fmt.Errorf("subscription belongs to another VM; state retained")
	}
	if !tatnet.V1VMSubscriptionUpdatePeriodType(result.JSON200.PeriodType).Valid() {
		return fmt.Errorf("unsupported subscription interval; state retained")
	}
	m.observeSubscription(result.JSON200)
	return nil
}

func (m *vmModel) observeSubscription(s *tatnet.V1VMSubscription) {
	m.SubscriptionPeriod = types.StringValue(s.PeriodType)
	m.AmountPerPeriod, m.Currency = types.StringValue(s.AmountPerPeriod), types.StringValue(s.Currency)
	m.AutoRenew = types.BoolValue(s.AutoRenew)
	if !m.BillingPeriod.IsNull() {
		m.BillingPeriod = types.StringValue(s.PeriodType)
	}
	if !m.PeriodDays.IsNull() {
		m.PeriodDays = types.Int64Null()
		if s.PeriodType == "1d" {
			m.PeriodDays = types.Int64Value(1)
		}
	}
	if !m.PeriodMonths.IsNull() {
		m.PeriodMonths = types.Int64Null()
		if strings.HasSuffix(s.PeriodType, "m") {
			v, _ := strconv.Atoi(strings.TrimSuffix(s.PeriodType, "m"))
			m.PeriodMonths = types.Int64Value(int64(v))
		}
	}
}
