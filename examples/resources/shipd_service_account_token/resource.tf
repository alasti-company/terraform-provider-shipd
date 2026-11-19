variable "worker_token_id" {
  type        = string
  description = "Generate a fresh UUIDv4 and retain it for this secret-store version."
}

variable "worker_token_version" {
  type        = string
  description = "Nonsecret, immutable version of the credential in your secret store."
}

variable "worker_token" {
  type      = string
  sensitive = true
  ephemeral = true
  default   = null
}

resource "shipd_service_account_token" "worker" {
  id                 = var.worker_token_id
  organization_id    = "org-id"
  service_account_id = "service-account-id"
  project_id         = "project-id"
  name               = "Worker"
  scopes             = { project = ["read"] }
  expires_at         = var.worker_token_expiry
  secret_version     = var.worker_token_version
  secret_wo          = var.worker_token
}

variable "worker_token_expiry" {
  type = string
}
