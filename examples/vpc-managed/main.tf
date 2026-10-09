terraform {
  required_providers {
    tatnet = {
      source  = "tatnet-ru/tatnet"
      version = "~> 0.3.0"
    }
  }
}

provider "tatnet" {}

variable "cluster_id" {
  type = string
}
variable "subnet" {
  type = string
}

resource "tatnet_vpc" "example" {
  cluster_id = var.cluster_id
  name       = "migration-pilot"
  subnet     = var.subnet

  lifecycle {
    prevent_destroy = true
  }
}

output "vpc_id" {
  value = tatnet_vpc.example.id
}
