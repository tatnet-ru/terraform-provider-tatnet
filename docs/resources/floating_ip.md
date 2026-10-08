---
page_title: "tatnet_floating_ip Resource - TatNet"
description: |-
  Allocate a public IPv4 address and optionally attach it to a VM interface.
---

# tatnet_floating_ip (Resource)

An account-scoped public IPv4 address in a region. Allocation requires a funded
balance and `floating_ip:read` / `floating_ip:write` permissions. **Billing
continues while detached and until the address is released.** This resource
manages manually allocated interface addresses, not VPC NAT gateways or
addresses still marked for automatic release.

## Example Usage

```terraform
resource "tatnet_floating_ip" "example" {
  cluster_id      = var.cluster_id
  name            = "application"
  vm_interface_id = tatnet_vm.example.primary_interface_id
}

output "public_ip" {
  value = tatnet_floating_ip.example.address
}
```

Omit `vm_interface_id` to reserve an unattached address. The reference to the VM
interface establishes ordering: create the VM before attachment, and release
its floating IP before destroying the VM. See `examples/floating-ip` for a
complete configuration.

## Schema

### Required

- `cluster_id` (String) Region UUID. Changes require replacement and can change the public address.

### Optional

- `name` (String) Label, 1–255 characters. Removing it clears the label in place.
- `vm_interface_id` (String) VM interface UUID in the same account and region, connected to a VPC. Changing it detaches, waits, then attaches to the new interface without releasing the address. Removing it detaches while retaining the paid allocation.

### Read-Only

- `id` (String) Address allocation UUID, used for import.
- `address` (String) Public IPv4 address.
- `status` (String) Observed networking status.

## Lifecycle and errors

Create retains the allocation ID before attaching. Failed or interrupted
attachment keeps the allocation in state; Terraform may mark a failed create
for replacement. The API has no idempotent allocation token: an ambiguous POST
is never retried automatically. Inspect the account before retrying a create
whose response did not include an ID.

Update reconciles the current API attachment, retaining observed partial state
if a later step fails. Delete detaches, waits for `available`, then releases.
Each operation has a ten-minute deadline, with five-second polling and a
30-second per-request HTTP timeout. An API error, timeout or cancellation
retains the resource for reconciliation; GET 404 removes it during refresh.
API error bodies and backend `last_error` are not included in diagnostics.

Use `lifecycle.prevent_destroy` when losing the public address would be
unacceptable. This does not stop billing. Do not also manage the same allocation
through another resource/state. A stopped or expired VM does not end address
billing.

## Import

Import an existing manually managed address by its allocation UUID:

```sh
terraform import tatnet_floating_ip.example FLOATING_IP_UUID
terraform plan
```

Match `cluster_id`, `name` and `vm_interface_id` to the existing allocation in
configuration before applying. Import and refresh do not detach or reconnect
it. Omitting its existing interface from configuration will plan a detach.
NAT gateway addresses and addresses with `auto_release=true` are rejected;
change their ownership/lifecycle explicitly outside this resource first.
