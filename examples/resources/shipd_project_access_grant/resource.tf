resource "shipd_project_access_grant" "reviewer" {
  project_id     = "project-id"
  principal_type = "member"
  principal_id   = "organization-membership-id"
  role           = "viewer"
}

resource "shipd_project_access_grant" "release_team" {
  project_id     = "project-id"
  principal_type = "team"
  principal_id   = "team-id"
  role           = "editor"
}
