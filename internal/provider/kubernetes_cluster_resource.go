package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

type kubernetesClusterResource struct {
	client                         *tatnet.ClientWithResponses
	pollInterval, operationTimeout time.Duration
}
type kubernetesClusterModel struct {
	ID                types.String `tfsdk:"id"`
	ProjectID         types.String `tfsdk:"project_id"`
	RegionID          types.String `tfsdk:"region_id"`
	Name              types.String `tfsdk:"name"`
	VMPlanID          types.String `tfsdk:"vm_plan_id"`
	K8sVersion        types.String `tfsdk:"k8s_version"`
	TalosVersion      types.String `tfsdk:"talos_version"`
	VPCID             types.String `tfsdk:"vpc_id"`
	EndpointAccess    types.String `tfsdk:"endpoint_access"`
	ControlPlaneCount types.Int64  `tfsdk:"control_plane_count"`
	WorkerCount       types.Int64  `tfsdk:"worker_count"`
	WorkerDiskSizeGB  types.Int64  `tfsdk:"worker_disk_size_gb"`
	Status            types.String `tfsdk:"status"`
	EndpointDNS       types.String `tfsdk:"endpoint_dns"`
	EndpointIP        types.String `tfsdk:"endpoint_ip"`
	EndpointPort      types.Int64  `tfsdk:"endpoint_port"`
	InternalVIP       types.String `tfsdk:"internal_vip"`
}

var _ resource.ResourceWithConfigure = (*kubernetesClusterResource)(nil)
var _ resource.ResourceWithValidateConfig = (*kubernetesClusterResource)(nil)
var _ resource.ResourceWithImportState = (*kubernetesClusterResource)(nil)
var errKubernetesNotFound = errors.New("Kubernetes cluster returned HTTP 404")

func NewKubernetesClusterResource() resource.Resource {
	return &kubernetesClusterResource{pollInterval: 5 * time.Second, operationTimeout: 45 * time.Minute}
}
func (*kubernetesClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}
func (*kubernetesClusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	attrs := map[string]schema.Attribute{}
	for _, name := range []string{"project_id", "region_id", "name", "vm_plan_id", "endpoint_access"} {
		attrs[name] = schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Explicit creation input. Changes replace the entire cluster."}
	}
	attrs["vpc_id"] = schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: "Existing VPC UUID; requires endpoint_access=private. Changes replace the cluster."}
	for _, name := range []string{"control_plane_count"} {
		attrs[name] = schema.Int64Attribute{Required: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}, Description: "Control-plane node count. Changes replace the cluster."}
	}
	attrs["talos_version"] = schema.StringAttribute{Required: true, Description: "Explicit Talos version. Changes upgrade in place to a newer regional v-prefixed full version; nodes reboot one at a time. Change separately from Kubernetes and worker count."}
	attrs["k8s_version"] = schema.StringAttribute{Required: true, Description: "Explicit Kubernetes version. Changes upgrade in place to a newer supported patch or next minor; downgrades and skipped minors are rejected."}
	attrs["worker_count"] = schema.Int64Attribute{Required: true, Description: "Worker count, 0–50. Changes scale the worker pool in place; scale-down drains and removes nodes."}
	attrs["worker_disk_size_gb"] = schema.Int64Attribute{Optional: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}, Description: "Worker disk override, 10–4000 GB. Omit to use the plan default. Changes replace the cluster."}
	attrs["id"] = schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
	for _, name := range []string{"status", "endpoint_dns", "endpoint_ip", "internal_vip"} {
		attrs[name] = schema.StringAttribute{Computed: true}
	}
	attrs["endpoint_port"] = schema.Int64Attribute{Computed: true}
	resp.Schema = schema.Schema{Description: "Manage a paid Kubernetes cluster with one control-plane pool and one worker pool using the same VM plan. Worker count changes scale in place and Kubernetes/Talos versions upgrade in place; other input changes replace the entire cluster. Use lifecycle.prevent_destroy for persistent workloads. Create/update/delete wait up to 45 minutes; interrupted operations retain identity. No kubeconfig is fetched.", Attributes: attrs}
}
func validateKubernetesConfig(m kubernetesClusterModel, known bool) error {
	for name, v := range map[string]types.String{"project_id": m.ProjectID, "region_id": m.RegionID, "vm_plan_id": m.VMPlanID, "vpc_id": m.VPCID} {
		if v.IsUnknown() {
			if known {
				return fmt.Errorf("%s must be known before creation", name)
			}
			continue
		}
		if v.IsNull() && name == "vpc_id" {
			continue
		}
		if !vpcUUID.MatchString(v.ValueString()) {
			return fmt.Errorf("%s must be a UUID", name)
		}
	}
	for name, v := range map[string]types.String{"name": m.Name, "k8s_version": m.K8sVersion, "talos_version": m.TalosVersion, "endpoint_access": m.EndpointAccess} {
		if v.IsUnknown() {
			if known {
				return fmt.Errorf("%s must be known before creation", name)
			}
			continue
		}
		if v.IsNull() || strings.TrimSpace(v.ValueString()) == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
	}
	if !m.Name.IsUnknown() && utf8.RuneCountInString(m.Name.ValueString()) > 255 {
		return fmt.Errorf("name must contain at most 255 characters")
	}
	if !m.K8sVersion.IsUnknown() && !kubernetesVersion.MatchString(m.K8sVersion.ValueString()) {
		return fmt.Errorf("k8s_version must be an explicit major.minor.patch string")
	}
	if !m.TalosVersion.IsUnknown() && !kubernetesTalosVersion.MatchString(m.TalosVersion.ValueString()) {
		return fmt.Errorf("talos_version must be an explicit major.minor.patch string, optionally prefixed with v")
	}

	if !m.EndpointAccess.IsUnknown() {
		switch m.EndpointAccess.ValueString() {
		case "public", "public_private", "private":
		default:
			return fmt.Errorf("endpoint_access must be public, public_private or private")
		}
		if !m.VPCID.IsNull() && !m.VPCID.IsUnknown() && m.EndpointAccess.ValueString() != "private" {
			return fmt.Errorf("a VPC requires endpoint_access=private")
		}
	}
	for name, v := range map[string]types.Int64{"control_plane_count": m.ControlPlaneCount, "worker_count": m.WorkerCount, "worker_disk_size_gb": m.WorkerDiskSizeGB} {
		if v.IsUnknown() {
			if known {
				return fmt.Errorf("%s must be known before creation", name)
			}
			continue
		}
		if name == "worker_disk_size_gb" && v.IsNull() {
			continue
		}
		n := v.ValueInt64()
		if v.IsNull() || (name == "control_plane_count" && n != 1 && n != 3) || (name == "worker_count" && (n < 0 || n > 50)) || (name == "worker_disk_size_gb" && (n < 10 || n > 4000)) {
			return fmt.Errorf("invalid %s", name)
		}
	}
	return nil
}
func (*kubernetesClusterResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m kubernetesClusterModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if !resp.Diagnostics.HasError() {
		if err := validateKubernetesConfig(m, false); err != nil {
			resp.Diagnostics.AddError("Invalid Kubernetes configuration", err.Error())
		}
	}
}
func (r *kubernetesClusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*tatnet.ClientWithResponses)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider client", "Expected a TatNet API client.")
	}
}
func (*kubernetesClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || !vpcUUID.MatchString(parts[0]) || !vpcUUID.MatchString(parts[1]) {
		resp.Diagnostics.AddError("Invalid Kubernetes import ID", "Use PROJECT_UUID/CLUSTER_UUID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
func (r *kubernetesClusterResource) get(ctx context.Context, m kubernetesClusterModel) (*tatnet.V1K8sCluster, error) {
	if r.client == nil {
		return nil, fmt.Errorf("provider is not configured")
	}
	if !vpcUUID.MatchString(m.ProjectID.ValueString()) || !vpcUUID.MatchString(m.ID.ValueString()) {
		return nil, fmt.Errorf("invalid project or cluster ID")
	}
	res, err := r.client.KubernetesGetClusterWithResponse(ctx, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		return nil, fmt.Errorf("Kubernetes request failed; check connectivity")
	}
	if res.StatusCode() == 404 {
		return nil, errKubernetesNotFound
	}
	if res.StatusCode() != 200 {
		return nil, fmt.Errorf("Kubernetes returned HTTP %d", res.StatusCode())
	}
	v := res.JSON200
	if v == nil || !strings.EqualFold(v.Id, m.ID.ValueString()) || !strings.EqualFold(v.ProjectId, m.ProjectID.ValueString()) || !vpcUUID.MatchString(v.RegionId) || strings.TrimSpace(v.Name) == "" || v.Status == "" || !kubernetesVersion.MatchString(v.K8sVersion) || !kubernetesTalosVersion.MatchString(v.TalosVersion) || v.EndpointPort < 1 || v.EndpointPort > 65535 {
		return nil, fmt.Errorf("Kubernetes returned invalid identity or metadata")
	}
	return v, nil
}
func preserveKubernetesUUID(previous types.String, value string) types.String {
	if strings.EqualFold(previous.ValueString(), value) {
		return previous
	}
	return types.StringValue(value)
}
func (m *kubernetesClusterModel) observe(v *tatnet.V1K8sCluster) error {
	if v.NodePools == nil || len(*v.NodePools) != 2 {
		return fmt.Errorf("cluster must have exactly one control-plane pool and one worker pool")
	}
	var cp, worker *tatnet.V1K8sNodePool
	for i := range *v.NodePools {
		p := &(*v.NodePools)[i]
		if !vpcUUID.MatchString(p.Id) || !vpcUUID.MatchString(p.VmPlanId) {
			return fmt.Errorf("invalid node pool metadata")
		}
		switch p.Role {
		case "controlplane":
			if cp != nil {
				return fmt.Errorf("duplicate control-plane pool")
			}
			cp = p
		case "worker":
			if worker != nil {
				return fmt.Errorf("duplicate worker pool")
			}
			worker = p
		default:
			return fmt.Errorf("unsupported node pool role")
		}
	}
	if cp == nil || worker == nil || strings.EqualFold(cp.Id, worker.Id) || !strings.EqualFold(cp.VmPlanId, worker.VmPlanId) {
		return fmt.Errorf("node pools must use one VM plan")
	}
	if v.EndpointAccess == nil {
		return fmt.Errorf("missing endpoint access mode")
	}
	m.ID = preserveKubernetesUUID(m.ID, v.Id)
	m.ProjectID = preserveKubernetesUUID(m.ProjectID, v.ProjectId)
	m.RegionID = preserveKubernetesUUID(m.RegionID, v.RegionId)
	m.VMPlanID = preserveKubernetesUUID(m.VMPlanID, cp.VmPlanId)
	m.Name = types.StringValue(v.Name)
	m.K8sVersion = types.StringValue(v.K8sVersion)
	m.TalosVersion = types.StringValue(v.TalosVersion)
	m.EndpointAccess = types.StringPointerValue(v.EndpointAccess)
	if v.VpcId == nil {
		m.VPCID = types.StringNull()
	} else {
		m.VPCID = preserveKubernetesUUID(m.VPCID, *v.VpcId)
	}
	m.ControlPlaneCount = types.Int64Value(int64(cp.DesiredCount))
	m.WorkerCount = types.Int64Value(int64(worker.DesiredCount))
	m.WorkerDiskSizeGB = types.Int64Null()
	if worker.DiskSizeGb != nil {
		m.WorkerDiskSizeGB = types.Int64Value(int64(*worker.DiskSizeGb))
	}
	if err := validateKubernetesConfig(*m, true); err != nil {
		return fmt.Errorf("cluster configuration cannot be represented: %s", err)
	}
	m.Status = types.StringValue(v.Status)
	m.EndpointDNS = types.StringPointerValue(v.EndpointDns)
	m.EndpointIP = types.StringPointerValue(v.EndpointIp)
	m.InternalVIP = types.StringPointerValue(v.InternalVip)
	m.EndpointPort = types.Int64Value(int64(v.EndpointPort))
	return nil
}
func kubernetesInputsEqual(a, b kubernetesClusterModel) bool {
	return a.ProjectID.Equal(b.ProjectID) && a.RegionID.Equal(b.RegionID) && a.VMPlanID.Equal(b.VMPlanID) && a.Name.Equal(b.Name) && a.K8sVersion.Equal(b.K8sVersion) && a.TalosVersion.Equal(b.TalosVersion) && a.VPCID.Equal(b.VPCID) && a.EndpointAccess.Equal(b.EndpointAccess) && a.ControlPlaneCount.Equal(b.ControlPlaneCount) && a.WorkerCount.Equal(b.WorkerCount) && a.WorkerDiskSizeGB.Equal(b.WorkerDiskSizeGB)
}
func (r *kubernetesClusterResource) operation(ctx context.Context) (context.Context, context.CancelFunc) {
	d := r.operationTimeout
	if d <= 0 {
		d = 45 * time.Minute
	}
	return context.WithTimeout(ctx, d)
}
func (r *kubernetesClusterResource) pause(ctx context.Context) error {
	d := r.pollInterval
	if d <= 0 {
		d = 5 * time.Second
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
func (r *kubernetesClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m kubernetesClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateKubernetesConfig(m, true); err != nil {
		resp.Diagnostics.AddError("Invalid Kubernetes configuration", err.Error())
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Configure TatNet before creating a cluster.")
		return
	}
	ctx, cancel := r.operation(ctx)
	defer cancel()
	cp, workers := int(m.ControlPlaneCount.ValueInt64()), int(m.WorkerCount.ValueInt64())
	body := tatnet.V1K8sClusterCreate{Name: m.Name.ValueString(), VmPlanId: m.VMPlanID.ValueString(), RegionId: m.RegionID.ValueStringPointer(), K8sVersion: m.K8sVersion.ValueStringPointer(), TalosVersion: m.TalosVersion.ValueStringPointer(), VpcId: m.VPCID.ValueStringPointer(), EndpointAccess: m.EndpointAccess.ValueStringPointer(), ControlPlaneCount: &cp, WorkerCount: &workers}
	if !m.WorkerDiskSizeGB.IsNull() {
		n := int(m.WorkerDiskSizeGB.ValueInt64())
		body.WorkerDiskSizeGb = &n
	}
	res, err := r.client.KubernetesCreateClusterWithResponse(ctx, m.ProjectID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Cannot create Kubernetes cluster", "Creation outcome unknown; inspect the project and import any created cluster before retrying. POST was not retried.")
		return
	}
	if res.StatusCode() != 201 || res.JSON201 == nil || !vpcUUID.MatchString(res.JSON201.Id) {
		resp.Diagnostics.AddError("Cannot create Kubernetes cluster", fmt.Sprintf("Create returned HTTP %d without a usable ID; inspect the project before retrying.", res.StatusCode()))
		return
	}
	// Persist the paid resource's identity even if the creation metadata or polling fails.
	m.ID = types.StringValue(res.JSON201.Id)
	m.Status = types.StringValue(res.JSON201.Status)
	m.EndpointDNS = types.StringNull()
	m.EndpointIP = types.StringNull()
	m.InternalVIP = types.StringNull()
	m.EndpointPort = types.Int64Null()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	planned := m
	for {
		v, e := r.get(ctx, m)
		if e != nil {
			resp.Diagnostics.AddError("Cannot reconcile Kubernetes cluster", e.Error()+"; created ID retained")
			return
		}
		observed := m
		if e = observed.observe(v); e != nil {
			resp.Diagnostics.AddError("Invalid Kubernetes response", e.Error()+"; created ID retained")
			return
		}
		if !kubernetesInputsEqual(planned, observed) {
			resp.Diagnostics.AddError("Kubernetes configuration mismatch", "API returned different creation inputs; created ID retained.")
			return
		}
		m = observed
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		if resp.Diagnostics.HasError() {
			return
		}
		poolsReady := true
		for _, pool := range *v.NodePools {
			if pool.Status == "error" {
				resp.Diagnostics.AddError("Kubernetes node provisioning failed", "Created ID retained; inspect node pools.")
				return
			}
			poolsReady = poolsReady && pool.Status == "active"
		}
		if v.Status == "ready" && poolsReady && (v.Upgrading == nil || !*v.Upgrading) {
			return
		}
		if v.Status == "error" || v.Status == "deleting" {
			resp.Diagnostics.AddError("Kubernetes provisioning failed", "Created ID retained; inspect the cluster.")
			return
		}
		if e = r.pause(ctx); e != nil {
			resp.Diagnostics.AddError("Kubernetes provisioning timeout", "Timed out or cancelled; created ID retained.")
			return
		}
	}
}
func (r *kubernetesClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m kubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.get(ctx, m)
	if errors.Is(err, errKubernetesNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err == nil {
		err = m.observe(v)
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read Kubernetes cluster", err.Error()+"; state retained")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Worker scaling leaves all other creation inputs immutable.
func kubernetesFixedInputsEqual(a, b kubernetesClusterModel) bool {
	a.WorkerCount = b.WorkerCount
	return kubernetesInputsEqual(a, b)
}
func kubernetesWorkerPool(v *tatnet.V1K8sCluster) *tatnet.V1K8sNodePool {
	for i := range *v.NodePools {
		if (*v.NodePools)[i].Role == "worker" {
			return &(*v.NodePools)[i]
		}
	}
	return nil // callers validate topology through observe first
}

// Pool status is derived from VM status and can briefly remain active after a
// scale-down request. Count ALL node rows, including those being drained, so an
// update cannot report completion before the excess nodes have been retired.
func (r *kubernetesClusterResource) scalingNodesConverged(ctx context.Context, m kubernetesClusterModel) (bool, error) {
	res, err := r.client.KubernetesListClusterNodesWithResponse(ctx, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil {
		return false, fmt.Errorf("Kubernetes node request failed; check connectivity")
	}
	if res.StatusCode() != 200 || res.JSON200 == nil {
		return false, fmt.Errorf("Kubernetes nodes returned HTTP %d without usable metadata", res.StatusCode())
	}
	ids, vms := map[string]bool{}, map[string]bool{}
	counts := map[string]int{"controlplane": 0, "worker": 0}
	pending := false
	for _, n := range *res.JSON200 {
		id := strings.ToLower(n.Id)
		if !vpcUUID.MatchString(n.Id) || ids[id] {
			return false, fmt.Errorf("invalid or duplicate node identity")
		}
		ids[id] = true
		if _, ok := counts[n.Role]; !ok {
			return false, fmt.Errorf("unsupported node role")
		}
		counts[n.Role]++
		if n.Status == "error" {
			return false, fmt.Errorf("Kubernetes node provisioning failed")
		}
		if n.Status == "" {
			return false, fmt.Errorf("missing node status")
		}
		pending = pending || n.Status == "removing" || n.Status == "removed" || n.VmId == nil
		if n.VmId != nil {
			vm := strings.ToLower(*n.VmId)
			if !vpcUUID.MatchString(vm) || vms[vm] {
				return false, fmt.Errorf("invalid or duplicate node VM identity")
			}
			vms[vm] = true
		}
	}
	return !pending && counts["controlplane"] == int(m.ControlPlaneCount.ValueInt64()) && counts["worker"] == int(m.WorkerCount.ValueInt64()), nil
}
func (r *kubernetesClusterResource) updateWorkers(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.State = req.State
	var planned, previous kubernetesClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	planned.ID = previous.ID
	if err := validateKubernetesConfig(planned, true); err != nil {
		resp.Diagnostics.AddError("Invalid Kubernetes configuration", err.Error())
		return
	}
	if !kubernetesFixedInputsEqual(previous, planned) {
		resp.Diagnostics.AddError("Kubernetes replacement required", "Only worker_count can be updated in place.")
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
		resp.Diagnostics.AddError("Cannot scale Kubernetes cluster", err.Error()+"; state retained")
		return
	}
	if !kubernetesFixedInputsEqual(planned, current) {
		resp.Diagnostics.AddError("Kubernetes configuration changed", "Refresh and reconcile non-worker inputs before scaling; state retained.")
		return
	}
	if v.Status != "ready" || (v.Upgrading != nil && *v.Upgrading) {
		resp.Diagnostics.AddError("Kubernetes cluster busy", "Scaling requires a ready cluster without an ongoing upgrade; state retained.")
		return
	}
	worker := kubernetesWorkerPool(v)
	poolID := worker.Id
	if !current.WorkerCount.Equal(planned.WorkerCount) {
		poolsActive := true
		for _, p := range *v.NodePools {
			poolsActive = poolsActive && p.Status == "active"
		}
		if !current.WorkerCount.Equal(previous.WorkerCount) || !poolsActive {
			resp.Diagnostics.AddError("Kubernetes worker pool changed", "Refresh or wait for the current scaling operation before requesting another count; state retained.")
			return
		}
		result, e := r.client.KubernetesScaleNodePoolWithResponse(ctx, current.ProjectID.ValueString(), current.ID.ValueString(), poolID, tatnet.V1K8sScale{DesiredCount: int(planned.WorkerCount.ValueInt64())})
		if e != nil {
			resp.Diagnostics.AddError("Cannot scale Kubernetes cluster", "Scale outcome unknown; refresh before retrying. POST was not retried; cluster identity retained.")
			return
		}
		p := result.JSON200
		if result.StatusCode() != 200 || p == nil || !strings.EqualFold(p.Id, poolID) || p.Role != "worker" || !strings.EqualFold(p.VmPlanId, worker.VmPlanId) || p.DesiredCount != int(planned.WorkerCount.ValueInt64()) || !types.Int64PointerValue(kubernetesInt64Pointer(p.DiskSizeGb)).Equal(planned.WorkerDiskSizeGB) {
			resp.Diagnostics.AddError("Cannot confirm Kubernetes scale request", fmt.Sprintf("Scale returned HTTP %d without matching worker pool metadata; refresh before retrying; state retained.", result.StatusCode()))
			return
		}
		// The API acknowledged this desired count. Keep it even if polling fails.
		current.WorkerCount = planned.WorkerCount
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
			resp.Diagnostics.AddError("Cannot reconcile Kubernetes scaling", err.Error()+"; state retained")
			return
		}
		if !kubernetesInputsEqual(planned, observed) || !strings.EqualFold(kubernetesWorkerPool(v).Id, poolID) {
			resp.Diagnostics.AddError("Kubernetes scaling configuration mismatch", "API returned different inputs or worker pool identity; refresh before retrying; state retained.")
			return
		}
		current = observed
		resp.Diagnostics.Append(resp.State.Set(ctx, &current)...)
		if resp.Diagnostics.HasError() {
			return
		}
		poolsReady := true
		for _, p := range *v.NodePools {
			if p.Status == "error" {
				resp.Diagnostics.AddError("Kubernetes scaling failed", "A node pool is in error; state retained.")
				return
			}
			poolsReady = poolsReady && p.Status == "active"
		}
		if v.Status == "error" || v.Status == "deleting" || (v.Upgrading != nil && *v.Upgrading) {
			resp.Diagnostics.AddError("Kubernetes scaling interrupted", "Cluster failed, is deleting or upgrading; state retained.")
			return
		}
		if v.Status == "ready" && poolsReady {
			ready, e := r.scalingNodesConverged(ctx, current)
			if e != nil {
				resp.Diagnostics.AddError("Cannot verify Kubernetes node count", e.Error()+"; state retained")
				return
			}
			if ready {
				return
			}
		}
		if e := r.pause(ctx); e != nil {
			resp.Diagnostics.AddError("Kubernetes scaling timeout", "Timed out or cancelled waiting for worker convergence; refresh before retrying; state retained.")
			return
		}
	}
}
func kubernetesInt64Pointer(n *int) *int64 {
	if n == nil {
		return nil
	}
	value := int64(*n)
	return &value
}
func (r *kubernetesClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m kubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := r.operation(ctx)
	defer cancel()
	v, err := r.get(ctx, m)
	if errors.Is(err, errKubernetesNotFound) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot delete Kubernetes cluster", err.Error()+"; state retained")
		return
	}
	if v.Status != "deleting" {
		res, e := r.client.KubernetesDeleteClusterWithResponse(ctx, m.ProjectID.ValueString(), m.ID.ValueString())
		if e != nil {
			resp.Diagnostics.AddError("Cannot delete Kubernetes cluster", "Deletion outcome unknown; refresh before retrying; state retained.")
			return
		}
		if res.StatusCode() != 202 && res.StatusCode() != 404 {
			resp.Diagnostics.AddError("Cannot delete Kubernetes cluster", fmt.Sprintf("Delete returned HTTP %d; state retained.", res.StatusCode()))
			return
		}
		if res.StatusCode() == 404 {
			return
		}
	}
	for {
		_, err = r.get(ctx, m)
		if errors.Is(err, errKubernetesNotFound) {
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("Cannot confirm Kubernetes deletion", err.Error()+"; state retained")
			return
		}
		if err = r.pause(ctx); err != nil {
			resp.Diagnostics.AddError("Kubernetes deletion timeout", "Timed out or cancelled waiting for HTTP 404; state retained.")
			return
		}
	}
}
