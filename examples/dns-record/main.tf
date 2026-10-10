# Development build: not included in the published 0.4.0 release.
terraform {
  required_providers {
    tatnet = { source = "tatnet-ru/tatnet" }
  }
}
provider "tatnet" {}
variable "zone_id" {
  type        = string
  description = "Account-accessible zone UUID."
}
variable "record_name" {
  type        = string
  description = "A dedicated lowercase absolute name within the zone."
  default     = "terraform-test.example.com."
}
variable "content" {
  type    = string
  default = "\"terraform-lifecycle-v1\""
}
resource "tatnet_dns_record" "test" {
  zone_id = var.zone_id
  name    = var.record_name
  type    = "TXT"
  content = var.content
}
output "record_id" { value = tatnet_dns_record.test.id }
output "rrset_ttl" { value = tatnet_dns_record.test.ttl }
