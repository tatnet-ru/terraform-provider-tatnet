terraform {
  required_providers {
    tatnet = {
      source  = "tatnet-ru/tatnet"
      version = "~> 0.3.0"
    }
  }
}

provider "tatnet" {}

variable "project_id" {
  type = string
}
variable "cluster_id" {
  type = string
}
variable "vm_plan_id" {
  type = string
}
variable "vpc_id" {
  type = string
}
variable "ssh_key_ids" {
  type = set(string)
}

data "tatnet_image" "debian" {
  project_id = var.project_id
  cluster_id = var.cluster_id
  family     = "debian"
  version    = "13"
}

resource "tatnet_vm" "example" {
  project_id   = var.project_id
  cluster_id   = var.cluster_id
  vm_plan_id   = var.vm_plan_id
  image_id     = data.tatnet_image.debian.id
  vpc_id       = var.vpc_id
  name         = "terraform-example"
  hostname     = "terraform-example"
  default_user = "debian"
  ssh_key_ids  = var.ssh_key_ids
  period_days  = 1
  auto_renew   = false
}

output "vm_id" {
  value = tatnet_vm.example.id
}

resource "tatnet_floating_ip" "example" {
  cluster_id      = var.cluster_id
  name            = "terraform-example"
  vm_interface_id = tatnet_vm.example.primary_interface_id
}

output "public_ip" {
  value = tatnet_floating_ip.example.address
}
