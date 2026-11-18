#!/usr/bin/env bash
# Apply this repository's GitHub settings: merge options, the default branch and
# the rulesets that enforce the branch flow (CONTRIBUTING.md). Safe to re-run:
# rulesets are updated in place by name.
#
#   scripts/setup-github.sh [owner/repo]     (default: the origin remote)
#
# Needs gh logged in as a repository admin. Rulesets are enforced on public
# repositories, or on private ones with GitHub Pro/Team.
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
command -v gh >/dev/null 2>&1 || die "gh (GitHub CLI) is required"

repo="${1:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}"
echo "== ${repo}"

# Merge commits for releases and back-merges, squash for everything else.
# Branches are not deleted on merge: release/X.Y.Z has two pull requests, and
# the release workflow deletes it once both are merged.
gh api -X PATCH "repos/${repo}" --silent \
  -F allow_merge_commit=true \
  -F allow_squash_merge=true \
  -F allow_rebase_merge=false \
  -F allow_auto_merge=true \
  -F delete_branch_on_merge=false \
  -f squash_merge_commit_title=PR_TITLE \
  -f squash_merge_commit_message=PR_BODY \
  -f merge_commit_title=MERGE_MESSAGE \
  -f merge_commit_message=PR_TITLE \
  -F has_wiki=false
gh api -X PATCH "repos/${repo}" --silent -f default_branch=dev
echo "merge settings set; default branch: dev"

# upsert <name> <json>: create the ruleset, or replace the one with that name.
upsert() {
  local name="$1" body="$2" id
  id="$(gh api "repos/${repo}/rulesets" --jq ".[] | select(.name == \"${name}\") | .id")"
  if [[ -n "$id" ]]; then
    gh api -X PUT "repos/${repo}/rulesets/${id}" --input - --silent <<< "$body"
    echo "ruleset updated: ${name}"
  else
    gh api -X POST "repos/${repo}/rulesets" --input - --silent <<< "$body"
    echo "ruleset created: ${name}"
  fi
}

# main: releases only. A pull request (merge commit) whose "test" and
# "release-gate" checks pass, on a branch up to date with main. release-gate
# fails for anything but this repository's release/X.Y.Z branches. No bypass,
# not even for admins.
upsert "main: releases only" '{
  "name": "main: releases only",
  "target": "branch",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": {"ref_name": {"include": ["refs/heads/main"], "exclude": []}},
  "rules": [
    {"type": "deletion"},
    {"type": "non_fast_forward"},
    {"type": "pull_request", "parameters": {
      "required_approving_review_count": 0,
      "dismiss_stale_reviews_on_push": false,
      "require_code_owner_review": false,
      "require_last_push_approval": false,
      "required_review_thread_resolution": true,
      "allowed_merge_methods": ["merge"]
    }},
    {"type": "required_status_checks", "parameters": {
      "strict_required_status_checks_policy": true,
      "required_status_checks": [{"context": "test"}, {"context": "release-gate"}]
    }}
  ]
}'

# dev: integration. Every change arrives by a pull request that passes "test".
# Not strict: a back-merge from a release branch need not be rebased onto dev.
upsert "dev: pull requests" '{
  "name": "dev: pull requests",
  "target": "branch",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": {"ref_name": {"include": ["refs/heads/dev"], "exclude": []}},
  "rules": [
    {"type": "deletion"},
    {"type": "non_fast_forward"},
    {"type": "pull_request", "parameters": {
      "required_approving_review_count": 0,
      "dismiss_stale_reviews_on_push": false,
      "require_code_owner_review": false,
      "require_last_push_approval": false,
      "required_review_thread_resolution": true,
      "allowed_merge_methods": ["squash", "merge"]
    }},
    {"type": "required_status_checks", "parameters": {
      "strict_required_status_checks_policy": false,
      "required_status_checks": [{"context": "test"}]
    }}
  ]
}'

# Release tags: never moved or deleted once the release workflow cuts them.
upsert "release tags" '{
  "name": "release tags",
  "target": "tag",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": {"ref_name": {"include": ["refs/tags/v*"], "exclude": []}},
  "rules": [
    {"type": "deletion"},
    {"type": "non_fast_forward"},
    {"type": "update"}
  ]
}'

echo "done"
