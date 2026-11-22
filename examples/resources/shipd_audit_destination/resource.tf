variable "audit_webhook_secret" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "audit_siem_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "audit_s3_secret_access_key" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "shipd_audit_destination" "webhook" {
  type               = "webhook"
  name               = "Audit webhook"
  url                = "https://receiver.example/audit"
  events             = ["flag.updated", "flag.created"]
  credential_version = "webhook-v1"
  credential_wo      = var.audit_webhook_secret
}

resource "shipd_audit_destination" "siem" {
  type               = "siem"
  name               = "Audit SIEM"
  url                = "https://siem.example/services/collector"
  format             = "splunk_hec"
  credential_version = "siem-v1"
  credential_wo      = var.audit_siem_token
}

resource "shipd_audit_destination" "s3" {
  type               = "s3"
  name               = "Audit archive"
  endpoint           = "https://s3.us-east-1.amazonaws.com"
  region             = "us-east-1"
  bucket             = "example-audit-archive"
  prefix             = "shipd"
  access_key_id      = "EXAMPLEACCESSKEYID"
  credential_version = "s3-v1"
  credential_wo      = var.audit_s3_secret_access_key
}
