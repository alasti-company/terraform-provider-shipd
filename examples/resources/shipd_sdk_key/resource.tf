variable "runtime_sdk_key_id" {
  type        = string
  description = "Generate a fresh UUIDv4 and retain it for this secret-store version."
}

variable "runtime_sdk_key_version" {
  type        = string
  description = "Nonsecret, immutable version of the credential in your secret store."
}

variable "runtime_sdk_key" {
  type      = string
  sensitive = true
  ephemeral = true
  default   = null
}

resource "shipd_sdk_key" "runtime" {
  id              = var.runtime_sdk_key_id
  organization_id = "org-id"
  project_id      = "project-id"
  environment_id  = "environment-id"
  name            = "Runtime"
  key_type        = "server"
  scopes          = ["snapshot:read"]
  secret_version  = var.runtime_sdk_key_version
  secret_wo       = var.runtime_sdk_key
}
