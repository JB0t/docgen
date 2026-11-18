# Changelog

All notable changes to docgen. Each release's section is written by `scripts/release.sh`
(see CONTRIBUTING.md); a `## Unreleased` section at the top, when present, becomes the
next release's notes.

## Unreleased

### Features
- feat: `gen`, `spec` and `validate`: boilerplate generation checked against retrieved specs and the real tools
- feat: environment overrides for a remote model server (`DOCGEN_LLM_*`), including `DOCGEN_LLM_API_KEY` as a Bearer token and `DOCGEN_LLM_EMBED_URL` / `DOCGEN_LLM_EMBED_MODEL`
- feat: `contrib/docgen-job` for a nightly Kubernetes CronJob; nightly drafts update existing docs only, without reference lists
- feat: a CLI or module at the repository root (`dir: .`, scope `.`)

### Fixes
- fix: plays without tasks no longer crash Ansible extraction
- fix: `check` no longer reports hidden commands, built environment variable names or runtime paths

### Other
- Standalone repository: module path `github.com/JB0t/docgen`, release binaries for Linux, macOS and Windows (amd64 and arm64); companion scripts in `contrib/`

## v1.0.0 - 2026-10-16

First stable release, made before docgen moved to its own repository.
