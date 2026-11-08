resource "shipd_project" "app" {
  name = "My application"
  slug = "my-application"
}

resource "shipd_environment" "production" {
  project_id    = shipd_project.app.id
  name          = "Production"
  slug          = "production"
  type          = "production"
  is_production = true

  lifecycle {
    prevent_destroy = true
  }
}

output "environment_id" {
  value = shipd_environment.production.id
}
