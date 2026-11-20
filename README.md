# shipd Terraform provider

The Terraform provider at `alasti-company/shipd` manages projects, project access
grants, environments, service accounts, webhooks, SDK keys, and workload service
account tokens. It also provides project and environment data sources. Credential
resources require a provider release that includes them and a shipd API with
credential registration support.

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
With development overrides, run `terraform plan` and `terraform apply` directly.
`terraform init` resolves the published provider in the Registry.

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

### Bootstrap service-account administration

In the shipd dashboard, sign in as an organization admin or owner and open
**Settings → Service accounts**. Create a dedicated account for Terraform, then
create a token on that account. Leave project and environment bindings empty
and grant the following scopes for full service-account management:

```json
{
  "service_account": ["read", "create", "update", "disable", "delete"]
}
```

Store the token as `SHIPD_API_KEY` in the CI secret store. Keep the bootstrap
account outside the Terraform configuration that uses its token. Use `read`
for import and refresh, `create` for provisioning, `update` for metadata,
`disable` for either enabled-state transition or for creating a disabled
account, and `delete` for destroy. Mutations are audited with the acting service
account ID and token ID.

The `shipd_service_account` resource requires a shipd API that supports
service-account administration with machine credentials.

## Project access grants

Use [`shipd_project_access_grant`](docs/resources/project_access_grant.md) to
manage one existing organization member's direct role or one team's assignment
to a project. Set `role` to `viewer` or `editor`. A team assignment can also use
`inherited` to follow shipd's organization-role inheritance rules. Role changes
update the grant in place. Other grants are managed independently.

The project must be on a Teams or Enterprise plan and the API must support
`project/getAccessGrant` and `project/changeAccessGrant`. An organization admin
or owner can issue a service-account token with these scopes:

```json
{
  "project_access": ["read", "manage"]
}
```

Use `read` for import and refresh, and `manage` for writes. A token can be
organization-wide or bound to one project. Leave its environment binding empty.
Organization owners and admins retain their organization authority, and removing
a direct grant can expose access inherited through a team or the organization.
Adding a first team assignment restricts organization-wide project access.
Removing the final team assignment restores it, subject to any direct grants.

For adoption, an organization admin can read existing grants from the project's
access settings or the `project/listMembers` and `project/listTeams` RPCs using
a user session. Use the returned `memberId` or `teamId`. Declare the grant with
its current role, using `inherited` when an existing team role is null, then import
it:

```sh
terraform import shipd_project_access_grant.reviewer PROJECT_ID/member/MEMBER_ID
terraform import shipd_project_access_grant.release_team PROJECT_ID/team/TEAM_ID
```

Use one Terraform address per project and principal. If a create response is
lost or invalid, the provider retries with the same idempotency key to recover
the API's recorded result. If the create remains unconfirmed, check shipd and
import the assignment if it exists. A create that encounters an existing
assignment returns a conflict and shows its import ID.

## Webhook configuration

- [Snapshot webhooks](docs/resources/snapshot_webhook.md) send
  `snapshot.published` to one Convex destination per environment. The URL and
  enabled state update in place. Use a service-account token with
  `environment:read` and `snapshot_webhook:read`, plus
  `snapshot_webhook:manage` for writes.
- [Event webhooks](docs/resources/event_webhook.md) send customer events to an
  HTTPS endpoint. Set `event_types` to the types you want to receive or use
  `"*"` for all types. Subscriptions and enabled state update in place.
  Changing the name or URL replaces the endpoint. Use a token with
  `event_webhook:read`, plus `event_webhook:manage` for writes. Your
  organization needs customer event webhooks enabled to create endpoints.

Both resources require shipd API support for the `createConfig` operations.
Moving either webhook to another environment replaces it. Destroy deletes the
webhook and, for event endpoints, cancels pending deliveries.

For a new receiver, create the webhook with `enabled = false`, rotate the
signing secret in the shipd dashboard, and save the returned secret in the
receiver's secret manager. Set `enabled = true` when the receiver is ready.
Terraform plans and state omit signing secrets. Imports and in-place updates
preserve the existing secret. Test sends, delivery history, and redelivery are
available in shipd.

Destination URLs are sensitive in Terraform output. Terraform stores the
complete URL in the plan and state. Use URLs without embedded credentials,
including credential-bearing paths and query strings.

## Ownership and lifecycle

- Terraform manages only resources declared in configuration. The data sources
  return existing objects without taking ownership.
- Projects expose `name` and `slug`. Environments expose `project_id`, `name`,
  `slug`, `type`, and `is_production`. IDs and API versions are computed.
- Changing an environment's project replaces it. Names and slugs update in place.
  `type` defaults to `development` and `is_production` defaults to `false`.
  Set both explicitly when importing environments with different values.
- Destroy archives projects and environments. Recovery and slug reservation last seven days.
  A subsequent create does not automatically restore or adopt an archived object.
  Restore through the dashboard and import it, choose a new slug, or wait for
  the reservation to expire. Project archive also makes child resources inaccessible.
- Use `lifecycle { prevent_destroy = true }` for important objects. Approval policies
  may independently block archive and production changes; the provider returns
  the API error and retains state. It does not approve requests.
- Only one environment per project may be production. To move the designation,
  first demote the current production environment and apply, then promote the
  replacement in another apply.
- External edits appear as drift. Project and environment updates use the version
  from refreshed state. Concurrent changes return a conflict and require a fresh plan.
- Service accounts expose `name`, `description`, and `enabled` (default `true`).
  Disable blocks all existing tokens. Re-enable restores access for unexpired,
  unrevoked tokens. Destroy permanently deletes the account and its tokens.
  Set `enabled = false` when importing a disabled account to preserve its state.
- Missing objects leave state; authorization and connection failures return errors
  instead.
- If creation of a project, environment, or service account commits before an
  interrupted run writes state, inspect shipd and import the existing ID before
  retrying. Credential registrations have their own recovery workflow.

Import existing resources:

```sh
terraform import shipd_project.app PROJECT_ID
terraform import shipd_environment.production PROJECT_ID/ENVIRONMENT_ID
terraform import shipd_service_account.build SERVICE_ACCOUNT_ID
terraform import shipd_snapshot_webhook.convex ENVIRONMENT_ID/DESTINATION_ID
terraform import shipd_event_webhook.invoices ENVIRONMENT_ID/ENDPOINT_ID
```


## Manage credentials from CI

| Resource | Provider authorization | Configuration |
| --- | --- | --- |
| [`shipd_sdk_key`](examples/resources/shipd_sdk_key) | Service account token with `sdk_key:create`, `sdk_key:read`, and `sdk_key:revoke` | Client or server key, one environment, explicit SDK scopes, optional expiry |
| [`shipd_service_account_token`](examples/resources/shipd_service_account_token) | Organization-wide service account token with `service_account_token:create`, `service_account_token:read`, and `service_account_token:revoke` | Existing workload account, optional project or environment binding, explicit scope map, required future expiry |

A bootstrap token must hold every workload scope it registers or revokes.
Workload token scopes exclude `service_account_token` and `apiKey`. Use
`SHIPD_API_KEY` for the bootstrap token and manage it in the dashboard. Existing
management API keys remain supported for provider authentication within their
bindings. Their creation and revocation use the user-session workflows.

Give each credential generation a lowercase UUIDv4 and an immutable secret
store version containing 32 random bytes encoded as unpadded base64url with the
appropriate prefix (`shipd_sdk_client_`, `shipd_sdk_server_`, or `shipd_sa_`).
Append a dot and the first 32 lowercase hex characters of the SHA-256 digest
of the organization ID. This binds the credential's lookup prefix to its
organization.
Set `secret_version` to the nonsecret version identifier and supply the secret
through a sensitive, ephemeral variable and the `secret_wo` argument. Consumers
load the same secret-store version after apply. The version identifier stays
in Terraform. Every new version needs a fresh UUID because Terraform cannot
diff the write-only secret.

The credential resources store IDs, names, bindings, scopes, expiry, SDK key
type, the secret version identifier, and revocation status in state. They store no plaintext, key prefix, or
hash and emit no secret outputs. Terraform's `sensitive` flag masks output.
The ephemeral variable and write-only argument keep these inputs out of saved
plans and state. Other resources or ordinary variables may persist their
values. The provider sends the key prefix and SHA-256 digest to shipd.

If creation commits before Terraform receives the response or writes state,
reapply with the same UUID, metadata, and secret-store version. The API
compares the UUID, lookup prefix, hash, and credential metadata, then retries SDK
edge publication for a match. A mismatch fails with a conflict. If you lose that secret-store version, confirm the registration
has settled and revoke the credential by UUID in shipd before issuing a fresh
UUID and secret. These credential resources do not support `terraform import`.

For rotation, create a second resource with a new UUID and secret. Apply, switch
the consumer, verify authentication, and remove the old resource in a later
apply. Both credentials work until you revoke them or either one expires. SDK
revocation follows edge propagation. Existing WebSocket connections
revalidate at least every 60 seconds. Replacing a single resource uses
Terraform's default destroy-first order. Use a fresh UUID for changes to any
immutable field and avoid `-replace` with the same UUID. Refresh records
out-of-band revocation. Expiry and revocation require explicit rotation.
