variable "runtime_sdk_key" {
  type      = string
  sensitive = true
  ephemeral = true
  default   = null
}

resource "shipd_sdk_key" "runtime" {
  id              = "42e909cc-c7a8-492e-98cc-0f2f91c25878"
  organization_id = "org-id"
  project_id      = "project-id"
  environment_id  = "environment-id"
  name            = "Runtime"
  key_type        = "server"
  scopes          = ["snapshot:read"]
  secret_wo       = var.runtime_sdk_key
}
