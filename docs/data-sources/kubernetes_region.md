---
page_title: "tatnet_kubernetes_region Data Source - terraform-provider-tatnet"
subcategory: "Kubernetes"
description: "Read offered Kubernetes and regional Talos versions."
---

# tatnet_kubernetes_region (Data Source)

Available since version 0.6.0. Reads `/v1/kubernetes/regions`, selecting
one exact region UUID and validating its catalog. Does not create a cluster or
retrieve credentials. Requires `k8s_cluster:read`.

```hcl
data "tatnet_kubernetes_region" "selected" {
  id = var.region_id
}
```

Required: `id` (region UUID).
Computed: `name`, `is_default`, `k8s_versions`, `default_k8s_version`,
`talos_versions`, `default_talos_version`.

Talos versions/default come from the selected region, not the global union.
An unavailable region, malformed catalog or failed request is a diagnostic;
API error bodies are not copied into diagnostics. Returned versions concern new
clusters; they do not prove workload compatibility or grant access to a live cluster.
