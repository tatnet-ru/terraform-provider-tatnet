terraform {
  required_providers {
    tatnet = {
      source = "tatnet-ru/tatnet"
    }
  }
}

provider "tatnet" {}

variable "zone_id" { type = string }
variable "record_id" { type = string }
variable "expected_zone_name" {
  type    = string
  default = "example.com."
}
variable "expected_record_name" {
  type    = string
  default = "pilot.example.com."
}
variable "expected_address" {
  type    = string
  default = "192.0.2.10"
}

# Development build: these data sources are not included in version 0.4.0.
data "tatnet_dns_zone" "existing" {
  id = var.zone_id
  lifecycle {
    postcondition {
      condition     = self.name == var.expected_zone_name
      error_message = "The zone UUID resolves to a different domain."
    }
  }
}

data "tatnet_dns_record" "existing" {
  id      = var.record_id
  zone_id = data.tatnet_dns_zone.existing.id
  lifecycle {
    postcondition {
      condition     = self.name == var.expected_record_name && self.type == "A" && self.content == var.expected_address
      error_message = "The record identity or address differs from the expected pilot."
    }
  }
}

output "record_ttl" { value = data.tatnet_dns_record.existing.ttl }
output "platform_managed" { value = data.tatnet_dns_record.existing.managed }
