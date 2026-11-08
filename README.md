# shipd Terraform provider

Terraform provider for shipd projects and environments, with data sources,
imports, and drift detection. This repository contains the standalone Go module
extracted from [`useshipd.com/providers/terraform`](https://github.com/alasti-company/useshipd.com/tree/main/providers/terraform).
The provider address is `alasti-company/shipd`; it is not published yet.
Flag definitions, targeting, credentials, and enterprise configuration are tracked
in [post-v0.1 issues #375–#384](https://github.com/alasti-company/useshipd.com/issues?q=is%3Aissue%20Terraform).

## Build and use locally

Requirements: Go 1.26.2 and Terraform 1.11 or later. CI covers Terraform 1.11.4
and 1.17.1. OpenTofu compatibility is not yet established.

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

The API must support `environment/updateWithoutHandoff` for environment updates.
This operation rejects implicit production demotion. Older servers reject the
operation instead of falling back to an update that might demote another environment.

## Credentials and permissions

Set `SHIPD_API_KEY` to a service-account token. `api_key` is also supported as a
sensitive provider argument. `SHIPD_API_URL` overrides the hosted API for local
or isolated testing. HTTPS is required except for localhost or loopback IP addresses.
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

Create the initial service account and token through the dashboard. Their
provisioning is outside v0.1. Existing management keys can be used within their
bindings, but cannot create projects. Project-bound service-account tokens can
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
  replacement in another apply. The API enforces this atomically, including
  concurrent writes. Existing dashboard handoffs keep their current behavior.
- External edits appear as drift. Updates use the version from refreshed state;
  concurrent changes return a conflict and require a fresh plan.
- Reads use the API's complete project/environment lists. Missing objects leave
  state; authorization and transport failures return diagnostics instead.
- Retries retain the same idempotency key within one operation. An interrupted
  process or exhausted response retries may leave a committed creation without
  Terraform state: inspect shipd and import the existing ID before retrying.

Import existing resources:

```sh
terraform import shipd_project.app PROJECT_ID
terraform import shipd_environment.production PROJECT_ID/ENVIRONMENT_ID
```

## Validation

From this directory:

```sh
go test -race ./...
go vet ./...
go tool tfplugindocs generate --provider-name shipd
terraform fmt -check -recursive examples
```

The normal Go tests run the real Terraform CLI against an in-memory HTTP fixture
and cover lifecycle, imports, drift, missing objects, organization guards, and
lookup validation. Client tests cover retries, conflict handling, cancellation,
and redirect behavior.

The CI workflow runs these checks on both supported Terraform versions and
verifies that generated Registry documentation matches the committed files.

### API acceptance tests

The [product repository's integration suite](https://github.com/alasti-company/useshipd.com/blob/main/apps/api/src/tests/terraform-provider.integration.test.ts)
provides the disposable Postgres/API harness and tests concurrent production
promotion and authorization boundaries. That harness stays in the product
repository.

To run this provider's acceptance test directly against an isolated API, set
`SHIPD_API_URL`, `SHIPD_API_KEY`, and `SHIPD_ORGANIZATION_ID` to a disposable
organization with the permissions above, then run:

```sh
TF_ACC=1 go test ./internal/provider -run '^TestAccTerraformLifecycle$' -count=1 -v
```

This test creates and archives a project and environment. The normal Go tests
use local HTTP fixtures and do not require hosted credentials.

## Public release preparation

Before publishing v0.1:

1. Choose a license for this standalone repository and confirm the Registry namespace.
2. Add a tag-triggered release workflow, configure its signing identity, and add the
   public signing key to the Terraform Registry. CI is included; automatic release
   publishing still needs to be configured.
3. Test the release pipeline with GoReleaser v2. The included configuration builds
   platform archives, a protocol-6 Registry manifest, and signed checksums. It expects
   `GPG_FINGERPRINT` and a usable signing key in the release environment.
   Build an unsigned local snapshot with
   `GORELEASER_CURRENT_TAG=v0.1.0 goreleaser release --snapshot --clean --skip=sign,publish,announce`.
4. Tag `v0.1.0`, publish the signed GitHub release, register the provider in the
   Terraform Registry, verify a clean `terraform init`, and update the product docs
   with the published address and pinned example version.
