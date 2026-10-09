---
page_title: "tatnet_vpc Resource - terraform-provider-tatnet"
subcategory: "Networking"
description: |-
  Manage an account-scoped IPv4 VPC.
---

# tatnet_vpc (Resource)

Available since version 0.3.0.
Requires `vpc:read` and `vpc:write`. No changes to customer image permissions.

```hcl
resource "tatnet_vpc" "application" {
  cluster_id = var.cluster_id
  name       = "application"
  subnet     = "10.42.0.0/24"

  lifecycle {
    prevent_destroy = true
  }
}
```

Use an available subnet approved for the selected region. The API checks reserved
ranges and overlaps. Reference `tatnet_vpc.application.id` from VM configuration.
An `active` VPC status confirms control-plane provisioning; it does not verify
application traffic or egress.

## Schema

Required: `cluster_id` (region UUID), `name` (1–255 characters), `subnet`
(canonical IPv4 CIDR without host bits). All three require replacement when
changed: the API has no VPC update endpoint. `prevent_destroy` blocks replacement
as well as destroy; remove it deliberately when replacing an empty test network.

Computed: `id`, `status`, `is_default` (null if the API omits it).

## Lifecycle and failure handling

Creation sends one POST, records the returned UUID, then polls for `active` for
up to ten minutes. Polling failures retain the UUID in state. Creation is not
idempotent: if the POST outcome is unknown or no usable UUID is returned, inspect
the account and import any created VPC before repeating apply.

Read removes state only on HTTP 404. Other API errors or malformed responses
retain state. Default networks can be inspected or imported but cannot be deleted
by this resource; use `data "tatnet_vpc"` to reference them.

Deletion freshly checks the API's default flag and NAT gateway. A missing default
flag, default VPC or existing NAT gateway blocks deletion. The provider does not
detach dependent resources. HTTP 409 retains state so dependencies can be removed
first. HTTP 204 or 404 completes deletion; OVN teardown is asynchronous.

**Backend requirement:** the API must reject occupied VPCs before publishing
OVN deletion for every network, including non-default VPCs. This guard was deployed
on 9 October 2026. Live tests on stage and production confirmed HTTP 409 and state
retention for a reserved IP. A complete production create → no-change plan → import
→ no-change plan → destroy succeeded; API HTTP 404 and empty state confirmed cleanup.
The tests used dedicated networks and no VMs or public IPs. They verify the API
lifecycle, not guest connectivity or completion of asynchronous OVN teardown.

## Import

```sh
terraform import tatnet_vpc.application VPC_UUID
terraform plan
```

Match the current region, name and subnet in configuration before apply.
Import only records the ID and reads the network; it does not rename it or create
another network. Changes to mismatched inputs would replace it.
