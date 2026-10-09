# tatnet_vpc (Data Source)

Reads an existing VPC accessible to the API key's account. Requires `vpc:read`.
Available since version 0.3.0.
No VPC, NAT gateway, or paid address is created or changed.

```hcl
data "tatnet_vpc" "existing" {
  id = var.vpc_id

  lifecycle {
    postcondition {
      condition     = self.cluster_id == var.cluster_id
      error_message = "The VPC must belong to the selected VM region."
    }
  }
}

# Use data.tatnet_vpc.existing.id as the VM's vpc_id.
```

## Schema

Required:

- `id` (String): existing VPC UUID.

Read-only:

- `cluster_id` (String): region UUID.
- `name` (String): display name.
- `subnet` (String): canonical IPv4 CIDR.
- `status` (String): observed API status; null if absent.
- `is_default` (Boolean): whether the API reports the account's regional default;
  null if absent.

Errors, including 403 and 404, fail the read. API error bodies are not exposed
in diagnostics. The response must match the selected ID and contain a valid
region and IPv4 subnet. This checks network identity and metadata; it does not
verify routing, NAT, firewall rules, available addresses, or connectivity.
