resource "shipd_service_account" "build" {
  name        = "build-automation"
  description = "Identity for the build pipeline"
  enabled     = true
}
