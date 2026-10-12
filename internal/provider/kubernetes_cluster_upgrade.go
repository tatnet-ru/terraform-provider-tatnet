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
	a.TalosVersion = b.TalosVersion
	a.WorkerCount = b.WorkerCount
	return kubernetesUpgradeInputsEqual(a, b)
}
func validateTalosUpgrade(current, target string) error {
	if !strings.HasPrefix(target, "v") {
		return fmt.Errorf("Talos upgrade target must be a v-prefixed full version offered in the region")
	}
	if err := validateKubernetesUpgrade(strings.TrimPrefix(current, "v"), strings.TrimPrefix(target, "v")); err != nil {
		return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), "Kubernetes", "Talos"))
	}
	return nil
}
func clusterUpgradeInputsEqual(a, b kubernetesClusterModel, talos bool) bool {
	if talos {
		a.TalosVersion = b.TalosVersion
		return kubernetesInputsEqual(a, b)
	}
	return kubernetesUpgradeInputsEqual(a, b)
}
func clusterUpgradeVersion(m kubernetesClusterModel, talos bool) string {
	if talos {
		return m.TalosVersion.ValueString()
	}
	return m.K8sVersion.ValueString()
}
func validateClusterUpgrade(current, target string, talos bool) error {
	if talos {
		return validateTalosUpgrade(current, target)
	}
	return validateKubernetesUpgrade(current, target)
}
func talosVersionCompare(a, b string) (int, error) {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	if !kubernetesVersion.MatchString(a) || !kubernetesVersion.MatchString(b) {
		return 0, fmt.Errorf("invalid observed Talos version")
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	x, y := [3]uint64{}, [3]uint64{}
	for i := range pa {
		var e error
		x[i], e = strconv.ParseUint(pa[i], 10, 32)
		if e != nil {
			return 0, e
		}
		y[i], e = strconv.ParseUint(pb[i], 10, 32)
		if e != nil {
			return 0, e
		}
	}
	for i := range x {
		if x[i] < y[i] {
			return -1, nil
		}
		if x[i] > y[i] {
			return 1, nil
		}
	}

	return 0, nil
}
func (*kubernetesClusterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var before, after kubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &before)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &after)...)
	if resp.Diagnostics.HasError() || !kubernetesImmutableInputsEqual(before, after) {
		return
	}
	kube := !before.K8sVersion.IsUnknown() && !after.K8sVersion.IsUnknown() && !before.K8sVersion.Equal(after.K8sVersion)
	talos := !before.TalosVersion.IsUnknown() && !after.TalosVersion.IsUnknown() && !before.TalosVersion.Equal(after.TalosVersion)
	workers := !before.WorkerCount.IsUnknown() && !after.WorkerCount.IsUnknown() && !before.WorkerCount.Equal(after.WorkerCount)
	if (kube && talos) || ((kube || talos) && workers) {
		resp.Diagnostics.AddError("Separate Kubernetes operations", "Change k8s_version, talos_version and worker_count in separate applies.")
		return
	}
	if kube || talos {
		if err := validateClusterUpgrade(clusterUpgradeVersion(before, talos), clusterUpgradeVersion(after, talos), talos); err != nil {
			resp.Diagnostics.AddError("Invalid cluster version upgrade", err.Error())
		}
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
	if !before.TalosVersion.Equal(after.TalosVersion) {
		r.updateClusterVersion(ctx, req, resp, true)
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
	r.updateClusterVersion(ctx, req, resp, false)
}
func (r *kubernetesClusterResource) updateClusterVersion(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse, talos bool) {
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
	if !clusterUpgradeInputsEqual(previous, planned, talos) {
		resp.Diagnostics.AddError("Separate Kubernetes operations", "Upgrade only one version; other inputs must remain unchanged.")
		return
	}
	if err := validateClusterUpgrade(clusterUpgradeVersion(previous, talos), clusterUpgradeVersion(planned, talos), talos); err != nil {
		resp.Diagnostics.AddError("Invalid Cluster version upgrade", err.Error())
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
		resp.Diagnostics.AddError("Cannot upgrade cluster version", err.Error()+"; state retained")
		return
	}
	if !clusterUpgradeInputsEqual(current, planned, talos) {
		resp.Diagnostics.AddError("Kubernetes configuration changed", "Refresh and reconcile other inputs before upgrading; state retained.")
		return
	}
	if v.Status != "ready" || v.Upgrading == nil {
		resp.Diagnostics.AddError("Cluster version upgrade unavailable", "Requires a ready cluster and explicit upgrading metadata; state retained.")
		return
	}
	poolIDs := kubernetesUpgradePoolIDs(v)
	if clusterUpgradeVersion(current, talos) != clusterUpgradeVersion(planned, talos) {
		if clusterUpgradeVersion(current, talos) != clusterUpgradeVersion(previous, talos) || *v.Upgrading {
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
		body := tatnet.V1K8sUpgrade{K8sVersion: planned.K8sVersion.ValueStringPointer()}
		if talos {
			body = tatnet.V1K8sUpgrade{TalosVersion: planned.TalosVersion.ValueStringPointer()}
			if v.NodeTalosVersion == nil {
				resp.Diagnostics.AddError("Talos observation unavailable", "Cannot verify the real OS upgrade floor; state retained.")
				return
			}
			cmp, e := talosVersionCompare(*v.NodeTalosVersion, current.TalosVersion.ValueString())
			if e != nil {
				resp.Diagnostics.AddError("Invalid Talos observation", "Cannot parse the real OS version; state retained.")
				return
			}
			floor := current.TalosVersion.ValueString()
			if cmp > 0 {
				floor = *v.NodeTalosVersion
			}
			if e = validateTalosUpgrade(floor, planned.TalosVersion.ValueString()); e != nil {
				resp.Diagnostics.AddError("Invalid Talos upgrade", e.Error()+"; state retained")
				return
			}
		}
		result, e := r.client.KubernetesUpgradeClusterWithResponse(ctx, current.ProjectID.ValueString(), current.ID.ValueString(), body)
		if e != nil {
			resp.Diagnostics.AddError("Cannot upgrade cluster version", "Upgrade outcome unknown; refresh before retrying. POST was not retried; cluster identity retained.")
			return
		}
		if result.StatusCode() != 200 || result.JSON200 == nil || result.JSON200.Status == "" || result.JSON200.EndpointPort < 1 || result.JSON200.EndpointPort > 65535 {
			resp.Diagnostics.AddError("Cannot confirm Cluster version upgrade", fmt.Sprintf("Upgrade returned HTTP %d without usable metadata; refresh before retrying; state retained.", result.StatusCode()))
			return
		}
		acknowledged := current
		if e = acknowledged.observe(result.JSON200); e != nil || !kubernetesInputsEqual(acknowledged, planned) || !acknowledged.ID.Equal(current.ID) || result.JSON200.Upgrading == nil || !kubernetesUpgradePoolsMatch(result.JSON200, poolIDs) {
			resp.Diagnostics.AddError("Invalid Cluster version upgrade response", "Response did not match the existing cluster and requested inputs; refresh before retrying; state retained.")
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
			resp.Diagnostics.AddError("Cannot reconcile Cluster version upgrade", err.Error()+"; state retained")
			return
		}
		if !kubernetesInputsEqual(observed, planned) || !kubernetesUpgradePoolsMatch(v, poolIDs) || v.Upgrading == nil {
			resp.Diagnostics.AddError("Cluster version upgrade configuration mismatch", "API returned different inputs, worker pool or missing upgrade progress; state retained.")
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
				resp.Diagnostics.AddError("Cluster version upgrade failed", "A node pool is in error; state retained.")
				return
			}
			active = active && p.Status == "active"
		}
		if v.Status == "error" || v.Status == "deleting" {
			resp.Diagnostics.AddError("Cluster version upgrade interrupted", "Cluster failed or is deleting; state retained.")
			return
		}
		observedTarget := true
		if talos {
			observedTarget = false
			if v.NodeTalosVersion != nil {
				cmp, e := talosVersionCompare(*v.NodeTalosVersion, planned.TalosVersion.ValueString())
				if e != nil || cmp > 0 {
					resp.Diagnostics.AddError("Invalid Talos observation", "Observed OS exceeds target or is malformed; inspect the cluster; state retained.")
					return
				}
				observedTarget = cmp == 0
			}
		}
		if v.Status == "ready" && active && !*v.Upgrading && observedTarget {
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
			resp.Diagnostics.AddError("Cluster version upgrade timeout", "Timed out or cancelled waiting for upgrading=false and node convergence; refresh before retrying; state retained.")
			return
		}
	}
}
