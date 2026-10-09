terraform {
  required_providers {
    tatnet = {
      source  = "tatnet-ru/tatnet"
      version = "~> 0.3.0"
    }
  }
}

# Export TATNET_API_KEY in your shell; do not put it in this file.
provider "tatnet" {}

variable "project_id" {
  type = string
}

variable "cluster_id" {
  type = string
}

data "tatnet_image" "debian" {
  project_id = var.project_id
  cluster_id = var.cluster_id
  family     = "debian"
  version    = "13"
}

output "image_id" {
  value = data.tatnet_image.debian.id
}
