terraform {
  required_providers {
    tatnet = {
      source  = "tatnet-ru/tatnet"
      version = ">= 0.5.0"
    }
  }
}
provider "tatnet" {}
variable "zone_id" {
  type = string
}
variable "name" {
  type    = string
  default = "terraform-set.example.com."
}
variable "ttl" {
  type    = number
  default = 300
}
variable "records" {
  type    = set(string)
  default = ["\"verification-one\"", "\"verification-two\""]
}
resource "tatnet_dns_rrset" "test" {
  zone_id = var.zone_id
  name    = var.name
  type    = "TXT"
  ttl     = var.ttl
  records = var.records
}
output "rrset_id" { value = tatnet_dns_rrset.test.id }
