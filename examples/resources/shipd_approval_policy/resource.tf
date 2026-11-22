variable "project_id" {
  type = string
}

variable "production_environment_id" {
  type = string
}

resource "shipd_approval_policy" "production" {
  name                   = "Production flag review"
  description            = "Require two reviewers for changes in production."
  project_ids            = [var.project_id]
  environment_ids        = [var.production_environment_id]
  environment_production = true
  required_approvals     = 2
  reviewer_roles         = ["owner", "admin"]
  allow_self_approval    = false
}
