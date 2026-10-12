# Changelog

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
