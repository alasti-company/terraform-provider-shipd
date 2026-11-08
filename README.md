# shipd Terraform provider

Terraform provider for shipd projects and environments, with data sources,
imports, and drift detection.
The provider address is `alasti-company/shipd`; it is not published yet.

## Local installation

Requirements: Go 1.26.2 and Terraform 1.11 or later.
OpenTofu compatibility is not yet established.

From this directory:

```sh
go build -o "$PWD/bin/terraform-provider-shipd" .
```

Create a local Terraform CLI configuration file, replacing the absolute path:

```hcl
provider_installation {
  dev_overrides {
    "alasti-company/shipd" = "/absolute/path/to/terraform-provider-shipd/bin"
  }
  direct {}
}
```

Point `TF_CLI_CONFIG_FILE` at that file. Use the configuration in
[`examples/provider`](examples/provider) together with a resource example.
With development overrides, run `terraform plan` and `terraform apply` directly;
`terraform init` still attempts to resolve the unpublished provider in the Registry.

## Credentials and permissions

Set `SHIPD_API_KEY` to a service-account token. `api_key` is also supported as a
sensitive provider argument. `SHIPD_API_URL` overrides the hosted API to connect
to another shipd instance. HTTPS is required except for localhost or loopback IP addresses.
The credential determines the organization;
`organization_id` / `SHIPD_ORGANIZATION_ID` optionally verifies it without switching.
Credentials must be known during planning.

An organization-wide token (no project or environment binding) can bootstrap
projects with these scopes:

```json
{
  "environment": ["read", "create", "update", "archive", "set_production"],
  "project": ["read", "create", "update", "archive"]
}
```

Create the initial service account and token through the dashboard. Existing
management keys can be used within their bindings, but cannot create projects.
Project-bound service-account tokens can
manage environments in that project; environment-bound credentials cannot create
environments. Read-only consumers need the respective `read` scopes.

## Ownership and lifecycle

- Terraform manages only resources declared in configuration. The data sources
  return existing objects without taking ownership.
- Projects expose `name` and `slug`. Environments expose `project_id`, `name`,
  `slug`, `type`, and `is_production`. IDs and API versions are computed.
- Changing an environment's project replaces it. Names and slugs update in place.
  `type` defaults to `development` and `is_production` defaults to `false`.
  Set both explicitly when importing environments with different values.
- Destroy archives the object. Recovery and slug reservation last seven days.
  A subsequent create does not automatically restore or adopt an archived object.
  Restore through the dashboard and import it, choose a new slug, or wait for
  the reservation to expire. Project archive also makes child resources inaccessible.
- Use `lifecycle { prevent_destroy = true }` for important objects. Approval policies
  may independently block archive and production changes; the provider returns
  the API error and retains state. It does not approve requests.
- Only one environment per project may be production. To move the designation,
  first demote the current production environment and apply, then promote the
  replacement in another apply.
- External edits appear as drift. Updates use the version from refreshed state;
  concurrent changes return a conflict and require a fresh plan.
- Missing objects leave state; authorization and connection failures return errors
  instead.
- An interrupted process or exhausted retries may leave a created resource without
  Terraform state: inspect shipd and import the existing ID before retrying.

Import existing resources:

```sh
terraform import shipd_project.app PROJECT_ID
terraform import shipd_environment.production PROJECT_ID/ENVIRONMENT_ID
```
