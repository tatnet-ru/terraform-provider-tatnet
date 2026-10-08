---
page_title: "tatnet_image Data Source - TatNet"
description: |-
  Select an account-accessible OS image deployed in a TatNet region.
---

# tatnet_image (Data Source)

Selects an OS image using the project's account permissions and regional
availability. Private images such as RED OS require an explicit grant on the
image for the account. An unavailable image produces an error.

## Example Usage

```terraform
data "tatnet_image" "debian" {
  project_id = var.project_id
  cluster_id = var.cluster_id
  family     = "debian"
  version    = "13"
}
```

## Schema

### Required

- `project_id` (String) Project UUID accessible to the API key.
- `cluster_id` (String) Target region UUID.
- `family` (String) OS family slug, for example `debian` or `redos`.
- `version` (String) Family version, for example `13` or `7.3`.

### Read-Only

- `id` (String) Permitted image UUID available in the requested region.

The regional `by_cluster` mapping determines the ID; the global image ID is not
used as a fallback. A new permitted regional build may change this value on the
next plan. If used by `tatnet_vm`, an image ID change requires VM replacement.
