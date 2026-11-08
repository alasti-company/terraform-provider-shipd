# Releasing shipd

Release Please maintains a release PR after releasable commits land on `main`.
Use Conventional Commit titles when squash-merging PRs: `fix:` bumps the patch
version, `feat:` bumps the minor version, and a breaking change such as `feat!:`
bumps the major version. Routine `ci:`, `docs:`, and `chore:` changes do not
trigger a version bump on their own.

To release, review the proposed version and changelog, wait for CI, then merge
the release PR. Release Please creates the `vX.Y.Z` tag and a draft GitHub
release, then explicitly dispatches `release.yml` at that tag. The release
workflow runs the CI matrix, builds and signs the provider assets, uploads them,
and publishes the draft. The Terraform Registry receives the published release
through its existing webhook.

The workflows use `GITHUB_TOKEN`; no additional personal access token is needed.
Release Please explicitly dispatches CI for its PR branches because bot-created
PR updates do not trigger the usual PR workflows automatically. Repository
Settings > Actions > General must allow GitHub Actions to create pull requests.
Publishing uses the existing `GPG_PRIVATE_KEY` and `PASSPHRASE` secrets.

If publishing fails, fix the failure and re-run the failed release workflow.
If the initial dispatch failed, run it explicitly at the existing tag:

```sh
gh workflow run release.yml --ref vX.Y.Z
```

Manual `v*` tag pushes still trigger the same release workflow. Never move a
published tag or replace published assets; release a new version instead.

Release Please starts from the published `v0.1.0` version. CI-only changes such
as this automation setup will not create a release PR until a releasable change
lands.
