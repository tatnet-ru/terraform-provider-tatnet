---
page_title: "TatNet Provider"
description: |-
  Manage TatNet virtual machines and select account-accessible images.
---

# TatNet Provider

The TatNet provider manages virtual machines through the public TatNet API.
Version 0.4.0 supports `tatnet_image`, `tatnet_vm`, `tatnet_floating_ip`
and `tatnet_vpc` as both a data source and a resource, plus `tatnet_nat_gateway`.

## Example Usage

```terraform
terraform {
  required_providers {
    tatnet = {
      source  = "tatnet-ru/tatnet"
      version = "~> 0.4.0"
    }
  }
}

# Set TATNET_API_KEY in the environment.
provider "tatnet" {}
```

A key belongs to one account. Image lookup requires project access and `vm:read`.
VM management additionally requires `vm:create` and `vm:delete`; the key issuer
must have billing management permission to create a paid VM.

## Schema

### Optional

- `api_key` (String, Sensitive) Account API key. Defaults to `TATNET_API_KEY`.
- `endpoint` (String) HTTPS API base URL. Defaults to `https://api.tatnet.ru/v1`.
  URLs with embedded credentials, query parameters or fragments are rejected.
  Redirects are not followed. Requests time out after 30 seconds.

Floating IP management requires account-scoped `floating_ip:read` and
`floating_ip:write` permissions. Addresses remain billed until released.

## VM limitations

VM input changes require replacement. Import, in-place updates, custom sizing,
cloud-init and power actions are not implemented.
Read the VM resource documentation before using persistent workloads.

## VPC support

Version 0.3.0 adds `tatnet_vpc` as both a data source and a resource. See the VPC resource documentation
for backend deletion-guard requirements, verified lifecycle and limitations.

## NAT support — version 0.4.0

Version 0.4.0 adds `tatnet_nat_gateway`. It allocates a paid public IP
and waits for automatic release on destroy. It is not included in version 0.3.0.
See its resource documentation for ownership, asynchronous deletion and live-test
limitations.

## Development DNS reads

The development build adds `tatnet_dns_zone` and `tatnet_dns_record` data
sources. Both read exact UUIDs; the record response must also match its parent
zone UUID. API metadata is distinct from DNS delegation/propagation. See the
DNS data-source documentation and examples/dns-read for postconditions.
These sources are not included in version 0.4.0 and perform no DNS writes.

The development `tatnet_dns_record` resource manages an individual record
with create/read/content-update/delete/import. Shared RRset TTL is read-only.
See its resource documentation for ownership, import and concurrency limits.
It is also not included in version 0.4.0.

The development `tatnet_dns_rrset` resource owns all values and their shared TTL.
Conditional revisions protect update/delete against concurrent edits. It is
not included in 0.4.0; see the resource documentation for ownership and import.
