package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

var _ resource.ResourceWithModifyPlan = (*kubernetesClusterResource)(nil)

func validateKubernetesUpgrade(current, target string) error {
	parse := func(v string) ([3]uint64, error) {
		result := [3]uint64{}
		if !kubernetesVersion.MatchString(v) {
			return result, fmt.Errorf("invalid Kubernetes version")
		}
		for i, p := range strings.Split(v, ".") {
			n, err := strconv.ParseUint(p, 10, 32)
			if err != nil {
				return result, fmt.Errorf("invalid Kubernetes version")
			}
			result[i] = n
		}
		return result, nil
	}
	from, err := parse(current)
	if err != nil {
		return err
	}
	to, err := parse(target)
	if err != nil {
		return err
	}
	if to[0] != from[0] || to[1] < from[1] || to[1] > from[1]+1 || (to[1] == from[1] && to[2] <= from[2]) {
		return fmt.Errorf("Kubernetes upgrades require a strictly newer patch or the next minor of the same major; downgrades and skipped minors are not supported")
	}
	return nil
}
func kubernetesUpgradeInputsEqual(a, b kubernetesClusterModel) bool {
	a.K8sVersion = b.K8sVersion
	return kubernetesInputsEqual(a, b)
}
func kubernetesImmutableInputsEqual(a, b kubernetesClusterModel) bool {
	a.WorkerCount = b.WorkerCount
	return kubernetesUpgradeInputsEqual(a, b)
}
func (*kubernetesClusterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var before, after kubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &before)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &after)...)
	if resp.Diagnostics.HasError() || before.K8sVersion.IsUnknown() || after.K8sVersion.IsUnknown() || before.K8sVersion.Equal(after.K8sVersion) {
		return
	}
	// Changes to an immutable input create a new cluster; its initial version
	// need not be newer than the cluster being replaced.
	if !kubernetesImmutableInputsEqual(before, after) {
		return
	}
	if !before.WorkerCount.IsUnknown() && !after.WorkerCount.IsUnknown() && !before.WorkerCount.Equal(after.WorkerCount) {
		resp.Diagnostics.AddError("Separate Kubernetes operations", "Change worker_count and k8s_version in separate applies.")
		return
	}
	if err := validateKubernetesUpgrade(before.K8sVersion.ValueString(), after.K8sVersion.ValueString()); err != nil {
		resp.Diagnostics.AddError("Invalid Kubernetes upgrade", err.Error())
	}
}
func (r *kubernetesClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.State = req.State
	var before, after kubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &before)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &after)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !before.K8sVersion.Equal(after.K8sVersion) {
		r.updateKubernetesVersion(ctx, req, resp)
		return
	}
	r.updateWorkers(ctx, req, resp)
}
func kubernetesUpgradePoolIDs(v *tatnet.V1K8sCluster) map[string]string {
	result := map[string]string{}
	for _, p := range *v.NodePools {
		result[p.Role] = strings.ToLower(p.Id)
	}
	return result
}
func kubernetesUpgradePoolsMatch(v *tatnet.V1K8sCluster, expected map[string]string) bool {
	for _, p := range *v.NodePools {
		if expected[p.Role] != strings.ToLower(p.Id) {
			return false
		}
	}
	return true
}
func (r *kubernetesClusterResource) updateKubernetesVersion(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var previous, planned kubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	if resp.Diagnostics.HasError() {
		return
	}
	planned.ID = previous.ID
	if err := validateKubernetesConfig(planned, true); err != nil {
		resp.Diagnostics.AddError("Invalid Kubernetes configuration", err.Error())
		return
	}
	if !kubernetesUpgradeInputsEqual(previous, planned) {
		resp.Diagnostics.AddError("Separate Kubernetes operations", "Upgrade only k8s_version; other inputs must remain unchanged.")
		return
	}
	if err := validateKubernetesUpgrade(previous.K8sVersion.ValueString(), planned.K8sVersion.ValueString()); err != nil {
		resp.Diagnostics.AddError("Invalid Kubernetes upgrade", err.Error())
		return
	}
	ctx, cancel := r.operation(ctx)
	defer cancel()
	v, err := r.get(ctx, previous)
	current := previous
	if err == nil {
		err = current.observe(v)
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot upgrade Kubernetes", err.Error()+"; state retained")
		return
	}
	if !kubernetesUpgradeInputsEqual(current, planned) {
		resp.Diagnostics.AddError("Kubernetes configuration changed", "Refresh and reconcile other inputs before upgrading; state retained.")
		return
	}
	if v.Status != "ready" || v.Upgrading == nil {
		resp.Diagnostics.AddError("Kubernetes upgrade unavailable", "Requires a ready cluster and explicit upgrading metadata; state retained.")
		return
	}
	poolIDs := kubernetesUpgradePoolIDs(v)
	if !current.K8sVersion.Equal(planned.K8sVersion) {
		if !current.K8sVersion.Equal(previous.K8sVersion) || *v.Upgrading {
			resp.Diagnostics.AddError("Kubernetes cluster changed", "Refresh or wait for the current upgrade before requesting another version; state retained.")
			return
		}
		for _, p := range *v.NodePools {
			if p.Status != "active" {
				resp.Diagnostics.AddError("Kubernetes node pool busy", "Wait for all pools to become active before upgrading; state retained.")
				return
			}
		}
		converged, e := r.scalingNodesConverged(ctx, current)
		if e != nil || !converged {
			resp.Diagnostics.AddError("Kubernetes nodes not converged", "Cannot verify the current node count before upgrading; state retained.")
			return
		}
		result, e := r.client.KubernetesUpgradeClusterWithResponse(ctx, current.ProjectID.ValueString(), current.ID.ValueString(), tatnet.V1K8sUpgrade{K8sVersion: planned.K8sVersion.ValueStringPointer()})
		if e != nil {
			resp.Diagnostics.AddError("Cannot upgrade Kubernetes", "Upgrade outcome unknown; refresh before retrying. POST was not retried; cluster identity retained.")
			return
		}
		if result.StatusCode() != 200 || result.JSON200 == nil || result.JSON200.Status == "" || result.JSON200.EndpointPort < 1 || result.JSON200.EndpointPort > 65535 {
			resp.Diagnostics.AddError("Cannot confirm Kubernetes upgrade", fmt.Sprintf("Upgrade returned HTTP %d without usable metadata; refresh before retrying; state retained.", result.StatusCode()))
			return
		}
		acknowledged := current
		if e = acknowledged.observe(result.JSON200); e != nil || !kubernetesInputsEqual(acknowledged, planned) || !acknowledged.ID.Equal(current.ID) || result.JSON200.Upgrading == nil || !kubernetesUpgradePoolsMatch(result.JSON200, poolIDs) {
			resp.Diagnostics.AddError("Invalid Kubernetes upgrade response", "Response did not match the existing cluster and requested inputs; refresh before retrying; state retained.")
			return
		}
		current = acknowledged
		resp.Diagnostics.Append(resp.State.Set(ctx, &current)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	for {
		v, err = r.get(ctx, current)
		observed := current
		if err == nil {
			err = observed.observe(v)
		}
		if err != nil {
			resp.Diagnostics.AddError("Cannot reconcile Kubernetes upgrade", err.Error()+"; state retained")
			return
		}
		if !kubernetesInputsEqual(observed, planned) || !kubernetesUpgradePoolsMatch(v, poolIDs) || v.Upgrading == nil {
			resp.Diagnostics.AddError("Kubernetes upgrade configuration mismatch", "API returned different inputs, worker pool or missing upgrade progress; state retained.")
			return
		}
		current = observed
		resp.Diagnostics.Append(resp.State.Set(ctx, &current)...)
		if resp.Diagnostics.HasError() {
			return
		}
		active := true
		for _, p := range *v.NodePools {
			if p.Status == "error" {
				resp.Diagnostics.AddError("Kubernetes upgrade failed", "A node pool is in error; state retained.")
				return
			}
			active = active && p.Status == "active"
		}
		if v.Status == "error" || v.Status == "deleting" {
			resp.Diagnostics.AddError("Kubernetes upgrade interrupted", "Cluster failed or is deleting; state retained.")
			return
		}
		if v.Status == "ready" && active && !*v.Upgrading {
			done, e := r.scalingNodesConverged(ctx, current)
			if e != nil {
				resp.Diagnostics.AddError("Cannot verify upgraded nodes", e.Error()+"; state retained")
				return
			}
			if done {
				return
			}
		}
		if e := r.pause(ctx); e != nil {
			resp.Diagnostics.AddError("Kubernetes upgrade timeout", "Timed out or cancelled waiting for upgrading=false and node convergence; refresh before retrying; state retained.")
			return
		}
	}
}
