# Changelog

## Unreleased

- Scale `tatnet_kubernetes_cluster.worker_count` in place, including zero workers; other input changes still require replacement.
- Verify the existing worker pool, wait for exact node counts and pool convergence, and retain cluster identity/acknowledged intent on errors. Never retry ambiguous scale requests.
- Cover scaling, recovery, API failures, immutable-input guards and Protocol 6 replacement behavior.
- Document worker removal, billing and readiness limits; `prevent_destroy` does not block scale-down.

## 0.3.0 — 2026-10-09

- Add the `tatnet_vpc` data source to read existing account VPCs and verify the VM region.
- Add the `tatnet_vpc` resource with create/read/delete/import. Creation waits for active status and retains the UUID on reconciliation failure; input changes require replacement.
- Block deletion of default VPCs and VPCs with NAT. API errors retain state; occupied-network rejection relies on the deployed backend guard.
- Preserve equivalent region UUID spelling on refresh to avoid unnecessary replacement.
- Add VPC examples, documentation, lifecycle/error tests and Protocol 6 replacement checks.

Live API lifecycle and reserved-IP deletion guards were verified on dedicated networks on stage and production. Test resources were removed. These checks do not verify guest connectivity or asynchronous OVN teardown.
