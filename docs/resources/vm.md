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
  period_months = 1
  auto_renew   = false
}
```

## Schema

### Required

Changes to VM identity, hardware, image and network inputs require replacement.
Payment period and automatic renewal update the existing subscription without
replacing or restarting the VM.

- `project_id` (String) Project UUID.
- `cluster_id` (String) Region UUID.
- `vm_plan_id` (String) VM plan UUID. Obtain from TatNet; the provider currently has no plan catalogue data source.
- `image_id` (String) Accessible image UUID deployed in the region.
- `vpc_id` (String) Existing VPC UUID in the same region.
- `name` (String) Display name.
- `hostname` (String) Hostname accepted by TatNet.
- `default_user` (String) SSH login user.
- `ssh_key_ids` (Set of String) At least one existing SSH key UUID.
- `auto_renew` (Boolean) Enable or disable automatic subscription renewal.

### Payment period

Specify exactly one of:

- `billing_period` (String) `1h`, `1d`, `1m`, `3m`, `6m`, or `12m`.
- `period_months` (Number) Calendar months: `1`, `3`, `6`, or `12`.
- `period_days` (Number) Only `1` is supported, for daily billing.

`period_days = 30` is rejected. For a calendar month use `period_months = 1`
or `billing_period = "1m"`. Rates and multi-month discounts are determined by
the server. Period changes refund unused prepaid time as bonus credit and
charge the new period; expired subscriptions must be renewed first. Changing
`auto_renew` alone does not charge or start a stopped VM.

### Read-Only

- `id` (String) VM UUID.
- `status` (String) Observed VM lifecycle status. Stopped VMs are not automatically started.
- `primary_interface_id` (String) UUID of the sole interface matching `vpc_id`, for `tatnet_floating_ip.vm_interface_id`. Null if the API reports no matching interface or more than one.
- `ipv4_addresses` (List of String) Observed IPv4 addresses, potentially in CIDR notation.
- `subscription_period` (String) Actual billing interval.
- `amount_per_period` (String) Total price per billing interval as a decimal string.
- `currency` (String) Currency of that price.

## Lifecycle and errors

Creation retains the returned ID before waiting for `active` (or `running`).
Deletion waits until GET returns 404. Both waits have a 20-minute limit and
poll every five seconds. An interrupted or failed creation retains the ID
when the API has returned one; Terraform may mark that instance for replacement.
HTTP 403 and server errors during refresh retain state.

Read refreshes name, hostname, status, addresses, payment period, price and
automatic renewal. It requires `vm:read` plus the API key creator's live
`account:view_billing` permission. Subscription updates require `vm:update`
plus `account:manage_billing`. Other creation inputs
remain as configured because the API does not return their complete original
values. A plan does not validate the plan's availability or account balance.

The API has no idempotent create token. A failed POST is never automatically
retried. If creation returns a transport error, check the project before
retrying to avoid an orphan VM.

Replacement deletes the VM and its disk. Consider `lifecycle.prevent_destroy`
for persistent workloads. Back up data before allowing replacement.

## Migrating existing subscriptions from provider 0.10.2

Deploy the subscription API before upgrading the provider. Replace the old
period setting in the existing resource; keep its Terraform address and VM
identity unchanged:

```terraform
# Remove: period_days = 30
period_months = 1
```

The new provider plans an in-place subscription update, including when the
old state contains `period_days = 30`. Check `terraform plan` for `~ update
in-place`, with no VM replacement. Version 0.10.2 itself cannot perform this
migration safely because its billing fields require replacement.

Reads retain the VM in state when billing is unavailable or access is denied.
Failed subscription updates do not retry automatically. Refresh before
retrying an uncertain update; repeating the same desired settings does not
charge another period. Changes to hardware or image still require replacement.

## Import

Import is not supported in this version.
