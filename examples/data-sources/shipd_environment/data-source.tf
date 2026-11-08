data "shipd_project" "app" {
  slug = "my-application"
}

data "shipd_environment" "production" {
  project_id = data.shipd_project.app.id
  slug       = "production"
}
