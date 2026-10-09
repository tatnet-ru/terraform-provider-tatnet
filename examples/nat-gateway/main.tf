terraform {
  required_providers {
    tatnet = {
      source  = "tatnet-ru/tatnet"
      version = ">= 0.4.0"
    }
  }
}

provider "tatnet" {}

variable "vpc_id" {
  type = string
}

# Requires provider 0.4.0 or later.
# Enabling NAT allocates a paid public IPv4 address.
resource "tatnet_nat_gateway" "example" {
  vpc_id = var.vpc_id

  lifecycle {
    prevent_destroy = true
  }
}

output "egress_address" {
  value = tatnet_nat_gateway.example.address
}
