variable "worker_token" {
  type      = string
  sensitive = true
  ephemeral = true
  default   = null
}

resource "shipd_service_account_token" "worker" {
  id                 = "ea76a78c-21a5-4e39-876a-27af888bf220"
  organization_id    = "org-id"
  service_account_id = "service-account-id"
  project_id         = "project-id"
  name               = "Worker"
  scopes             = { project = ["read"] }
  expires_at         = var.worker_token_expiry
  secret_wo          = var.worker_token
}

variable "worker_token_expiry" {
  type = string
}
