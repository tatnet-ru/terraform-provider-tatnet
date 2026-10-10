---
page_title: "tatnet_dns_rrset Resource - TatNet"
description: |-
  Atomically manage all values and the shared TTL of a DNS RRset.
---

# tatnet_dns_rrset (Resource)

Available from provider version 0.5.0. Requires the atomic RRset API
and `dns_zone:read` plus `dns_zone:write` permissions on the account zone.

```terraform
resource "tatnet_dns_rrset" "web" {
  zone_id = var.zone_id
  name    = "app.example.com."
  type    = "A"
  ttl     = 300
  records = ["192.0.2.10", "192.0.2.20"]
}
```

## Schema

Required: `zone_id` (zone UUID), `name` (lowercase absolute DNS name with trailing
dot), `type` (`A`, `AAAA`, `CNAME`, `TXT`), `records` (set of 1..256 API
presentation strings). Name, type or zone changes require replacement.
CNAME requires one lowercase absolute target; TXT presentation values may be
quoted. Content whitespace/newlines are rejected.

Optional: `ttl` (integer 0..2147483647). Omitted or null explicitly inherits
the zone default. Every value in a RRset has the same effective TTL.

Computed: `id`, `managed`, `revision` (opaque last-observed API revision).

## Ownership and concurrency

This resource owns the complete set: an update replaces all its values and TTL
in one database transaction. Creation never upserts an existing set; import it
explicitly. Platform-managed sets cannot be changed or deleted.

Do not manage the same data through `tatnet_dns_record`, another RRset resource,
another Terraform state or an uncoordinated external writer. Unchanged values
retain their API record UUIDs, but removed values are deleted.

Update and delete use the revision from state, verified before the request and
checked atomically by the API. An edit between plan/read and mutation returns
409 and preserves state. The provider does not silently rebase the revision or
retry a write. Review concurrent edits before making a new plan. Ordinary
Terraform refresh observes current external edits, so review planned deletions
and full-set replacements carefully, including imported data.

Create persists a scoped usable UUID before reporting response mismatches.
Read 404 removes state; other failures preserve it. After an uncertain create,
inspect the zone and import the result before retrying. After uncertain
update/delete, refresh first. API success does not verify DNS propagation;
check both authoritative servers before traffic cutover.

## Import

```shell
terraform import tatnet_dns_rrset.web ZONE_UUID/RRSET_UUID
```

Supply the complete existing value set and its TTL, then inspect the plan.
Destroy deletes the whole RRset and all its values, including imported values.
