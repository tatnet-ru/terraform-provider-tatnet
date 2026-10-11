# Development feature; use dev_overrides until a release includes Kubernetes.
terraform {
  required_providers {
    tatnet = { source = "tatnet-ru/tatnet" }
  }
}
provider "tatnet" {}
variable "project_id" { type = string }
variable "region_id" { type = string }
variable "vm_plan_id" { type = string }
variable "vpc_id" { type = string }
variable "k8s_version" { type = string }
variable "talos_version" { type = string }

data "tatnet_kubernetes_region" "selected" {
  id = var.region_id
}
data "tatnet_vpc" "selected" {
  id = var.vpc_id
}
resource "tatnet_kubernetes_cluster" "billing" {
  project_id          = var.project_id
  region_id           = var.region_id
  name                = "billing-kubernetes"
  vm_plan_id          = var.vm_plan_id
  vpc_id              = data.tatnet_vpc.selected.id
  endpoint_access     = "private"
  k8s_version         = var.k8s_version
  talos_version       = var.talos_version
  control_plane_count = 1
  worker_count        = 2

  lifecycle {
    prevent_destroy = true
    precondition {
      condition     = contains(data.tatnet_kubernetes_region.selected.k8s_versions, var.k8s_version)
      error_message = "Kubernetes version must be offered by TatNet."
    }
    precondition {
      condition     = contains(data.tatnet_kubernetes_region.selected.talos_versions, var.talos_version)
      error_message = "Talos version must have a ready image in this region."
    }
    precondition {
      condition     = lower(data.tatnet_vpc.selected.cluster_id) == lower(var.region_id)
      error_message = "VPC must belong to the Kubernetes region."
    }
  }
}
output "cluster_id" {
  value = tatnet_kubernetes_cluster.billing.id
}
output "private_api_address" {
  value = tatnet_kubernetes_cluster.billing.internal_vip
}
