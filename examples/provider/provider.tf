terraform {
  required_version = ">= 1.11"
  required_providers {
    shipd = {
      source = "alasti-company/shipd"
    }
  }
}

# Set SHIPD_API_KEY to a service-account token scoped to this configuration.
# An organization-wide token is required to create projects.
provider "shipd" {}
