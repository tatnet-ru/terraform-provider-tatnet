terraform {
  required_providers {
    tatnet = {
      source  = "tatnet-ru/tatnet"
      version = "~> 0.3.0"
    }
  }
}

provider "tatnet" {}

variable "vpc_id" {
  type = string
}
variable "cluster_id" {
  type = string
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

output "vpc_subnet" {
  value = data.tatnet_vpc.existing.subnet
}
