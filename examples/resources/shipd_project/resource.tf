resource "shipd_project" "app" {
  name = "My application"
  slug = "my-application"
}

output "project_id" {
  value = shipd_project.app.id
}
