terraform {
  required_version = ">= 1.11"
  required_providers {
    shipd = {
      source = "alasti-company/shipd"
    }
  }
}

# Set SHIPD_API_KEY to an organization-wide service-account token.
provider "shipd" {}
