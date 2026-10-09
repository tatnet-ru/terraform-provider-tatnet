---
page_title: "tatnet_nat_gateway Resource - terraform-provider-tatnet"
subcategory: "Networking"
description: |-
  Manage VPC egress NAT and its allocated public IPv4 address.
---

# tatnet_nat_gateway (Resource)

Available from provider version 0.4.0. Live lifecycle,
import, DNS/HTTPS egress without a VM public IP and complete test cleanup were
verified on 2026-10-09.

Allocate a new public IPv4 address and SNAT the VPC subnet through it. One NAT
gateway is allowed per VPC. **The IP is billed until released**, including while
provisioning or being detached. Requires `vpc:read`, `vpc:write` and
`floating_ip:read`; new allocation also passes the API funds/quota checks.

```hcl
resource "tatnet_nat_gateway" "application" {
  vpc_id = tatnet_vpc.application.id

  lifecycle {
    prevent_destroy = true
  }
}
```

The VPC must be active and have no gateway, including a gateway still detaching.
An existing gateway is never implicitly adopted: import the exact allocation.
The resource always allocates a new IP; it does not take an IP from
`tatnet_floating_ip`, because disabling NAT automatically releases that address.
Do not manage the same IP through both resources.

## Schema

Required: `vpc_id` (VPC UUID). Changes require replacement, which releases the
old IP and allocates another. `prevent_destroy` also blocks replacement.

Computed: `id` (owned floating IP UUID), `cluster_id` (region UUID), `address`
(public IPv4), `status`, `enabled` (observed API intent).

The resource uses `POST /vpcs/{id}/nat-gateway`, `GET /vpcs/{id}`,
`DELETE /vpcs/{id}/nat-gateway` and `GET /floating-ips/{id}`. Creation waits up
to ten minutes for `attached`; that status does not prove DNS or internet access
from a VM. The resource does not alter VM routes, DNS settings or firewall rules.

## Failure handling and deletion

Creation sends one POST and records a usable allocation UUID before polling.
Failures during polling retain the ID in state. If the POST result is unknown
or has no usable UUID, inspect the VPC/account and import any allocated gateway
before retrying; POST is not retried automatically.

Deletion reads the VPC and checks that its gateway still uses the owned IP.
A different allocation blocks deletion. DELETE also sends `expected_fip_id`
with the owned allocation UUID. The API checks this condition under a row lock:
a gateway replaced after the read returns HTTP 409 and remains enabled.
It disables the matching gateway once, then
waits for both the VPC gateway to disappear and the owned IP to return HTTP 404.
HTTP 202 or an already disabled gateway alone does not complete deletion.
The API's automatic release runs asynchronously; timeouts and API failures
retain state so the paid allocation remains tracked. Retry after reconciliation.
The provider never explicitly releases a manually retained or retargeted IP.

Read only removes state once gateway absence and allocation HTTP 404 agree.
An IP being released remains tracked as `detaching`. A missing VPC with a paid
allocation, an ownership mismatch or an API error requires reconciliation.
Observed provisioning errors remain visible in `status`.

Requires an API deployment supporting conditional NAT disable via
`expected_fip_id`. Use one Terraform owner for each gateway; an ownership
conflict requires reconciliation and is never retried as an unconditional DELETE.

## Import

```sh
terraform import tatnet_nat_gateway.application VPC_UUID/FLOATING_IP_UUID
terraform plan
```

Use the current VPC UUID in configuration. Import records both identities and
reads the gateway; it does not enable NAT or allocate an IP. Importing authorizes
this resource to disable the gateway and let the API release its IP on destroy.

Unit tests cover lifecycle, polling, billing/auth errors, ambiguous writes,
identity checks, state retention, import and replacement. A paid live test must
verify the full lifecycle and egress from a VM before a public release; this
acceptance test passed on 2026-10-09.
