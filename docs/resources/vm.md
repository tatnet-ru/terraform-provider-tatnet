---
page_title: "tatnet_vm Resource - TatNet"
description: |-
  Manage a prepaid TatNet VM in an existing private network.
---

# tatnet_vm (Resource)

Creates a prepaid VM using the CPU, memory and disk defaults of an existing
plan. One DHCP interface is attached to the specified VPC. No public IP is
allocated by the VM resource; use `tatnet_floating_ip` for a separate address. Creation charges account funds for the selected period.

## Example Usage

```terraform
resource "tatnet_vm" "example" {
  project_id   = var.project_id
  cluster_id   = var.cluster_id
  vm_plan_id   = var.vm_plan_id
  image_id     = data.tatnet_image.debian.id
  vpc_id       = var.vpc_id
  name         = "terraform-example"
  hostname     = "terraform-example"
  default_user = "debian"
  ssh_key_ids  = var.ssh_key_ids
  period_days  = 1
  auto_renew   = false
}
```

## Schema

### Required

All input changes require replacement, including billing settings and name.

- `project_id` (String) Project UUID.
- `cluster_id` (String) Region UUID.
- `vm_plan_id` (String) VM plan UUID. Obtain from TatNet; the provider currently has no plan catalogue data source.
- `image_id` (String) Accessible image UUID deployed in the region.
- `vpc_id` (String) Existing VPC UUID in the same region.
- `name` (String) Display name.
- `hostname` (String) Hostname accepted by TatNet.
- `default_user` (String) SSH login user.
- `ssh_key_ids` (Set of String) At least one existing SSH key UUID.
- `period_days` (Number) Positive integer initial prepaid period in days.
- `auto_renew` (Boolean) Enable or disable automatic subscription renewal.

### Read-Only

- `id` (String) VM UUID.
- `status` (String) Observed VM lifecycle status. Stopped VMs are not automatically started.
- `primary_interface_id` (String) UUID of the sole interface matching `vpc_id`, for `tatnet_floating_ip.vm_interface_id`. Null if the API reports no matching interface or more than one.
- `ipv4_addresses` (List of String) Observed IPv4 addresses, potentially in CIDR notation.

## Lifecycle and errors

Creation retains the returned ID before waiting for `active` (or `running`).
Deletion waits until GET returns 404. Both waits have a 20-minute limit and
poll every five seconds. An interrupted or failed creation retains the ID
when the API has returned one; Terraform may mark that instance for replacement.
HTTP 403 and server errors during refresh retain state.

Read refreshes name, hostname, status and addresses. Other creation inputs
remain as configured because the API does not return their complete original
values. A plan does not validate the plan's availability or account balance.

The API has no idempotent create token. A failed POST is never automatically
retried. If creation returns a transport error, check the project before
retrying to avoid an orphan VM.

Replacement deletes the VM and its disk. Consider `lifecycle.prevent_destroy`
for persistent workloads. Back up data before allowing replacement.

## Import

Import is not supported in this version.
