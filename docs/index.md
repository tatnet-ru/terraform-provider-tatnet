---
page_title: "TatNet Provider"
description: |-
  Manage TatNet virtual machines and select account-accessible images.
---

# TatNet Provider

The TatNet provider manages virtual machines through the public TatNet API.
Version 0.2.0 supports `tatnet_image`, `tatnet_vm` and `tatnet_floating_ip`.

## Example Usage

```terraform
terraform {
  required_providers {
    tatnet = {
      source  = "tatnet-ru/tatnet"
      version = "~> 0.2.0"
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
