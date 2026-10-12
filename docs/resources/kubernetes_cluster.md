---
page_title: "tatnet_kubernetes_cluster Resource - terraform-provider-tatnet"
subcategory: "Kubernetes"
description: "Manage a TatNet Kubernetes cluster with explicit versions and node topology."
---

# tatnet_kubernetes_cluster (Resource)

Available since version 0.6.0. Supports create/read/delete/import
for a paid cluster with one control-plane pool and one worker pool using the same
VM plan. Requires `k8s_cluster:create/read/delete`, plus `k8s_cluster:update` for
worker scaling, and sufficient account funds.
Cluster creation can allocate billable nodes and a public API endpoint, depending
on endpoint mode. Terraform plan does not verify funds or VM-plan availability.

Since version 0.7.0, **`worker_count` changes scale the existing worker pool in
place**. In the next release, `k8s_version` also upgrades in place. All remaining input
changes replace the entire cluster, including `control_plane_count`,
`talos_version`, VM plan and disk override. Version 0.6.0 still
replaces the cluster on worker-count changes. Kubernetes upgrade is added in the next release; Talos upgrades remain unsupported. For persistent workloads
use `lifecycle { prevent_destroy = true }`, and inspect every plan. Cluster deletion
is not a backup or a migration strategy for workload data/PVs.

```hcl
resource "tatnet_kubernetes_cluster" "example" {
  project_id          = var.project_id
  region_id           = var.region_id
  vm_plan_id          = var.vm_plan_id
  name                = "billing-kubernetes"
  k8s_version         = var.k8s_version
  talos_version       = var.talos_version
  control_plane_count = 1
  worker_count        = 2
  vpc_id              = var.vpc_id
  endpoint_access     = "private"

  lifecycle {
    prevent_destroy = true
  }
}
```

Use `tatnet_kubernetes_region` to check offered Kubernetes versions and regional
Talos images. Pin versions explicitly; do not bind persistent clusters directly
to changing catalog defaults. See [the complete example](../../examples/kubernetes/main.tf)
for version and VPC-region preconditions.

## Schema

Required: `project_id`, `region_id`, `vm_plan_id` (UUIDs), `name` (1–255 characters),
`k8s_version` (major.minor.patch), `talos_version` (major.minor.patch, optionally
prefixed with `v`; use the exact regional catalog spelling), `control_plane_count`
(1 or 3), `worker_count` (0–50), `endpoint_access` (`public`, `public_private`, `private`).

Optional: `vpc_id` (UUID; requires private endpoint), `worker_disk_size_gb`
(10–4000 GB, omitted uses the VM-plan default). Region and plan compatibility
are enforced by the API; version availability is checked in the example.

Computed: `id`, `status`, `endpoint_dns`, `endpoint_ip`, `endpoint_port`, `internal_vip`.
Absent optional endpoint metadata is null. A private endpoint needs an actual
route from the operator into the cluster network/VPC; a read-only plan does not
prove this route. The provider never downloads kubeconfig or cluster-admin credentials.

## Lifecycle and failures

Create issues one POST and persists the returned valid UUID before polling.
It waits for cluster `ready`, both pools `active`, and no ongoing upgrade.
These are API observations, not a workload/CSI/CCM/ingress smoke test.
Create, update and delete have a 45-minute operation deadline, with five-second polling.

No mutation is automatically retried. An ambiguous POST without a usable result
requires checking project clusters and importing the created cluster before retrying.
Polling errors, mismatched creation inputs, timeout or cancellation retain the created
ID; Terraform may mark a failed creation for replacement. Inspect/reconcile it first.

Read verifies project/cluster identity and the supported two-pool topology.
Only HTTP 404 removes state; HTTP 403/500 or malformed responses retain it.
External changes are reflected in state when representable; heterogeneous VM plans
or additional pools require manual reconciliation rather than silently guessing inputs.

### Worker scaling (since 0.7.0)

Changing only `worker_count` (0–50) sends one POST to the existing worker pool's
scale endpoint; it never creates or deletes the cluster. Increasing the count
creates billable VMs. Decreasing it asks the platform to drain and remove excess
workers. Scaling to zero leaves control-plane nodes running; workloads then have
no worker capacity. `prevent_destroy` prevents cluster replacement/deletion but
**does not prevent worker removal**, Pod disruption or downtime during scaling.
Plan capacity and disruption budgets before reducing the count.

Scaling requires a ready cluster without an ongoing upgrade. A new desired count
is rejected if either pool is busy or the observed worker intent has changed
since the previous state. If the API already has the requested desired count,
Update skips the POST and waits for convergence. Concurrent Terraform/operator
writes are not serialized by the provider; use one owner for this worker pool.

Update verifies unchanged creation inputs and worker-pool identity. It waits for
ready/active API status and exactly the requested worker and control-plane node
rows, with unique VM identities and no draining/removing nodes. Counting rows
prevents a temporarily stale `active` status from completing scale-down early.
API pool status uses VM state; this is **not proof of Kubernetes Node Ready,
workload readiness, successful PVC relocation, or physical/billing cleanup**.

An acknowledged desired count and the cluster identity remain in state if polling
fails or times out. An ambiguous or malformed response is never retried; refresh
and inspect the pool/nodes before another apply. Refresh reads desired intent,
so a later no-op plan does not by itself prove an interrupted scale has converged.
No kubeconfig or controller credentials are requested during scaling.

Delete skips a repeated DELETE if the cluster is already deleting, and waits for
GET 404 after HTTP 202. Errors/timeouts retain state. GET 404 proves control-plane
absence only; the live lifecycle test must separately verify VM, address, volume
and billing cleanup before claiming complete infrastructure teardown.

## Import

```sh
terraform import tatnet_kubernetes_cluster.example PROJECT_UUID/CLUSTER_UUID
```

Import does not provision or reconfigure the cluster. Read reconstructs inputs
from the cluster and its pools; only the supported simple topology can be represented.
Write matching configuration and inspect plan before applying.

## Live acceptance

Version 0.7.0 worker scaling was tested on a dedicated Kubernetes 1.34.9 /
Talos v1.13.5 cluster with a single control plane and a worker cycle of 1 → 2 → 1.
Both Terraform plans contained only an in-place update. Cluster identity and
endpoint stayed unchanged, and each apply was followed by a no-op plan.

Scale-up retained the running workload Pod and its PVC. A single-replica
Deployment was then placed on the newly added worker with preferred node affinity.
Scale-down moved it back to the original worker; the same PVC/volume and both
synced files were preserved, with one active attachment. All remaining nodes
were Kubernetes Ready. The removed worker VM/root disks and billing subscription
were independently verified as deleted. Zero-worker scaling is unit-tested;
it was not exercised in this live workload test. Abrupt node failure and
PodDisruptionBudget-blocked drains were not tested.

## Kubernetes version upgrade (next release)

Change only `k8s_version` to a newer supported patch or the next minor of the
same major. Plan rejects downgrades, skipped minor versions and a simultaneous
`worker_count` change. Other immutable inputs can still cause cluster replacement;
use `prevent_destroy` and inspect the plan. The API checks target availability.

Update requires a ready cluster, active pools, the existing node count and
explicit `upgrading` metadata. It issues one project-scoped POST with only the
Kubernetes target, preserving Talos and worker count. It validates unchanged
inputs and both pool identities, then waits for `upgrading=false`, ready/active
status and exact node counts. A retry with the same API target waits without
another POST. A different ongoing target or missing progress metadata is rejected.

Errors retain cluster identity and the acknowledged target. Neither an ambiguous
POST nor a polling failure causes another mutation. Refresh reads desired version;
a no-op plan alone cannot prove that an interrupted upgrade completed. Completion
uses API observations, not direct kubelet/Pod inspection. A live test must check
actual Kubernetes node versions, Ready, Deployment and PVC data independently.
The provider never downloads kubeconfig or controller keys. Upgrade and worker
scaling must be separate applies. Upgrade waits up to 45 minutes; workload
availability depends on topology and disruption handling.
