resource "shipd_snapshot_webhook" "convex" {
  environment_id = "your-environment-id"
  url            = "https://your-deployment.convex.site/shipd/webhook"
  enabled        = false
}
