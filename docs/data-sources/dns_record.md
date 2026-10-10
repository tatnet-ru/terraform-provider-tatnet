---
page_title: "tatnet_dns_record Data Source - terraform-provider-tatnet"
subcategory: "DNS"
description: |-
  Read one existing DNS record by its zone UUID and record UUID.
---

# tatnet_dns_record (Data Source)

Available from provider version 0.5.0.
Requires `dns_zone:read` on the parent zone.

```hcl
data "tatnet_dns_record" "existing" {
  zone_id = data.tatnet_dns_zone.existing.id
  id      = var.record_id
}
```

Required: `zone_id`, `id` (UUIDs). Both are checked against the response.
Computed: `name`, `type`, `content`, `ttl`, `managed`.

Names include the trailing dot. Content is returned as stored by the API;
this is one flat record, not a complete RRset. TTL is shared by records in
the RRset and may be null when inherited. `managed` indicates platform
ownership and is null if omitted; reading a managed record does not transfer
ownership to Terraform. No API writes or recursive/authoritative DNS queries
are performed. A missing record or access failure is an error.

Use postconditions to assert the expected domain, name, type and content
before preparing DNS changes. The [example](../../examples/dns-read/main.tf)
performs those checks without creating resources. Protect Terraform state,
which contains the record's content.
