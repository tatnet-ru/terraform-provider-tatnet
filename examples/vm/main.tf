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

data "tatnet_vpc" "existing" {
  id = var.vpc_id

  lifecycle {
    postcondition {
      condition     = self.cluster_id == var.cluster_id
      error_message = "The VPC must belong to the selected VM region."
    }
  }
}

resource "tatnet_vm" "example" {
  project_id   = var.project_id
  cluster_id   = var.cluster_id
  vm_plan_id   = var.vm_plan_id
  image_id     = data.tatnet_image.debian.id
  vpc_id       = data.tatnet_vpc.existing.id
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
