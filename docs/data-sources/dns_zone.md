---
page_title: "tatnet_dns_zone Data Source - terraform-provider-tatnet"
subcategory: "DNS"
description: |-
  Read an existing account-accessible DNS zone by UUID.
---

# tatnet_dns_zone (Data Source)

Development build only; not included in published version 0.4.0.
Requires `dns_zone:read` on the selected zone. Reads one exact UUID; it does
not create a zone, verify delegation, change DNSSEC, or adopt records.

```hcl
data "tatnet_dns_zone" "existing" {
  id = var.zone_id
}
```

Required: `id` (zone UUID).

Computed: `name` (absolute name including trailing dot), `status`,
`default_ttl`, `dnssec_enabled`. Omitted optional API fields remain null.
Zone status is API metadata; independently verify authoritative DNS before
moving traffic. The returned UUID must match the requested UUID; 404 and
authorization errors are diagnostics, not an empty zone.

See [the DNS read example](../../examples/dns-read/main.tf) for domain and
record postconditions. DNS names and record contents remain in Terraform state;
protect state access and do not use these data sources to read secret TXT data
into an unprotected state file.
