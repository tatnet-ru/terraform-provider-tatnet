terraform {
  required_providers {
    tatnet = { source = "tatnet-ru/tatnet" }
  }
}
provider "tatnet" {}
variable "region_id" {
  type = string
}
data "tatnet_kubernetes_region" "selected" {
  id = var.region_id
}
output "offered_kubernetes_versions" {
  value = data.tatnet_kubernetes_region.selected.k8s_versions
}
output "offered_regional_talos_versions" {
  value = data.tatnet_kubernetes_region.selected.talos_versions
}
