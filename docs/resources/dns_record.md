---
page_title: "tatnet_dns_record Resource - TatNet"
description: |-
  Manage the content of one non-platform-managed DNS record.
---

# tatnet_dns_record (Resource)

Development build only; not included in the published 0.4.0 release.
Requires `dns_zone:read` and `dns_zone:write` on the selected account zone.

## Example Usage

```terraform
resource "tatnet_dns_record" "verification" {
  zone_id = var.zone_id
  name    = "terraform-test.example.com."
  type    = "TXT"
  content = "\"verification-value\""
}
```

## Schema

### Required

- `zone_id` (String) Zone UUID. Changes require replacement.
- `name` (String) Lowercase absolute name with a trailing dot. Changes require replacement.
- `type` (String) `A`, `AAAA`, `CNAME` or `TXT`. Changes require replacement.
- `content` (String) Nonempty API presentation content without surrounding whitespace or newlines.
  CNAME targets must be lowercase absolute names. Content changes update in place.

### Read-Only

- `id` (String) Record UUID.
- `ttl` (Number) Shared RRset TTL. Null means zone default.
- `managed` (Boolean) Platform ownership flag. Writes require an explicit false from the API.

## Ownership and TTL

This resource owns one record UUID, not an entire RRset. It never sends TTL,
name or type on update. Creation omits TTL, preserving an existing RRset's TTL
or inheriting the zone default for a new RRset. Changing a shared TTL requires
explicit management of the complete RRset; TTL configuration is intentionally
not offered on this individual-record resource.

Existing records are never silently adopted. A duplicate creation fails with
HTTP 409; import explicitly to manage an existing record. Never manage a single
record and its entire RRset in separate resources or Terraform states.
Platform-managed records cannot be modified or deleted. Before update/delete,
the provider verifies the UUID, parent zone, original name/type and ownership.
The API also enforces ownership. These checks do not provide transactional
protection against concurrent edits; coordinate writers until an atomic RRset
contract is available. API success does not confirm DNS export or propagation.

Create saves a scoped usable ID before reporting response mismatches. Read 404
removes the record from state; other failures preserve state. No automatic
write retry is made. After an uncertain create, inspect the zone and import any
created record before retrying. After an uncertain update/delete, refresh first.

## Import

```shell
terraform import tatnet_dns_record.verification ZONE_UUID/RECORD_UUID
```

Configure the imported record's current name, type and content and review the
plan before apply. Destroy deletes that record, including imported records.
The final record's removal also removes its now-empty RRset in the API.
