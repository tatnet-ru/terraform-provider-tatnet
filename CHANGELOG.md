# Changelog

## Unreleased

- Add calendar VM payment periods (`period_months = 1/3/6/12`) and explicit `billing_period` (`1h`, `1d`, `1m`, `3m`, `6m`, `12m`). Reject unsupported daily values before creating a VM.
- Update VM subscription intervals and automatic renewal in place. Preserve VM identity, disks and power state; retain state on API errors without retrying mutations.
- Refresh actual subscription period, price, currency and automatic renewal. Document migration from `period_days = 30` without replacing an existing VM.
- Pin SDK v0.16.2. Deploy the VM subscription API before releasing this provider.

## 0.10.1 — 2026-10-21

- Add independent `tatnet_vm_backup_policy` with daily UTC schedule, retention, drift refresh, import and storage usage/rate.
- Destroy reads the current full policy before disabling captures, preserving external schedule/retention edits and recovery points. Existing backups must be explicitly removed before VM deletion.
- Pin SDK v0.14.0.


## 0.9.0 — 2026-10-12

- Upgrade `talos_version` in place through a target-only request, with Kubernetes/Talos/scaling in separate applies.
- Check the actual observed OS floor and target before completion; prevent silent downgrade from stale pins, missing observations or skipped minors.
- Share guarded version Update without automatic mutation retries; cover OS upgrade/recovery/failure behavior and existing Kubernetes/scaling regressions.

Live Talos v1.13.4→v1.13.5 upgrade verified all three actual OS versions and changed boot IDs, Ready nodes/Deployment, unchanged Kubernetes 1.34.9 and cluster/endpoint/VM/PVC identity, with both synced files intact. HA continuity and next-minor Talos live upgrades were not tested.

## 0.8.0 — 2026-10-12

- Upgrade `tatnet_kubernetes_cluster.k8s_version` in place using the project-scoped API. Talos and other immutable inputs still require replacement.
- Reject downgrades, skipped minors and combined upgrade/scaling plans; wait for explicit upgrade completion, stable pool identity and node counts.
- Retain acknowledged intent and identity on failures without retrying ambiguous POSTs. Cover upgrade/recovery/failure behavior and Protocol 6 planning guards.

Live 1.33.13→1.34.9 upgrade verified API server and all three kubelets at the target version, Ready nodes/Deployment, and stable cluster/endpoint/VM/Talos/PVC identity with both synced files preserved. Patch-only upgrade paths remain unit-tested.

## 0.7.0 — 2026-10-12

- Scale `tatnet_kubernetes_cluster.worker_count` in place, including zero workers; other input changes still require replacement.
- Verify the existing worker pool, wait for exact node counts and pool convergence, and retain cluster identity/acknowledged intent on errors. Never retry ambiguous scale requests.
- Cover scaling, recovery, API failures, immutable-input guards and Protocol 6 replacement behavior.
- Document worker removal, billing and readiness limits; `prevent_destroy` does not block scale-down.

Live 1→2→1 worker scaling preserved cluster identity, endpoint, PVC and two synced files. The Deployment relocated away from the removed worker; its VM/root disks and billing were verified as deleted. Scale-to-zero remains unit-tested only.

## 0.3.0 — 2026-10-09

- Add the `tatnet_vpc` data source to read existing account VPCs and verify the VM region.
- Add the `tatnet_vpc` resource with create/read/delete/import. Creation waits for active status and retains the UUID on reconciliation failure; input changes require replacement.
- Block deletion of default VPCs and VPCs with NAT. API errors retain state; occupied-network rejection relies on the deployed backend guard.
- Preserve equivalent region UUID spelling on refresh to avoid unnecessary replacement.
- Add VPC examples, documentation, lifecycle/error tests and Protocol 6 replacement checks.

Live API lifecycle and reserved-IP deletion guards were verified on dedicated networks on stage and production. Test resources were removed. These checks do not verify guest connectivity or asynchronous OVN teardown.
