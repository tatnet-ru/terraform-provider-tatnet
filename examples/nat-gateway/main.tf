terraform {
  required_providers {
    tatnet = {
      source = "tatnet-ru/tatnet"
    }
  }
}

provider "tatnet" {}

variable "vpc_id" {
  type = string
}

# Development build: not available in the published 0.3.0 provider.
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
