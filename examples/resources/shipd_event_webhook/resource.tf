resource "shipd_event_webhook" "invoices" {
  environment_id = "your-environment-id"
  name           = "Invoice events"
  url            = "https://receiver.example/webhooks"
  event_types    = ["invoice.paid", "invoice.voided"]
  enabled        = false
}
