# Contributing

## Branches

| Branch | Holds | Changes arrive by |
| --- | --- | --- |
| `main` | Released code. Every commit on it is a release, tagged `vX.Y.Z`. | A pull request from `release/X.Y.Z` only, merged with a merge commit. |
| `dev` | The next release. The default branch. | A pull request whose tests pass (squash or merge). |
| `release/X.Y.Z` | One release being cut. | `scripts/release.sh`, plus any fix for that release. |
| anything else (`feat/...`, `fix/...`) | Work in progress. | Pushes. |

The rulesets that enforce this are in `scripts/setup-github.sh`:

- `main`: no direct pushes, force pushes or deletion. A pull request merges only when the
  `test` and `release-gate` checks pass and the branch is up to date with `main`.
  `release-gate` fails for anything but this repository's `release/X.Y.Z` branches.
  There is no bypass, including for admins.
- `dev`: no direct pushes, force pushes or deletion. A pull request merges when `test` passes.
- `v*` tags: never moved or deleted.

## Making a change

1. Branch from `dev`: `git switch -c feat/my-change origin/dev`.
2. Commit with [Conventional Commits](https://www.conventionalcommits.org/) subjects:
   `feat: ...` (new command or flag: minor), `fix: ...` (patch), `feat!: ...` or a
   `BREAKING CHANGE:` footer (major). Anything else (`docs:`, `chore:`, `refactor:`) is a patch.
   The subjects become the release notes, and they pick the next version.
3. Open a pull request into `dev`. CI runs gofmt, `go mod tidy`, `go vet`, `go test -race` and a
   cross-compile for Linux, macOS and Windows (amd64 and arm64).

## Releasing

```bash
scripts/release.sh --dry-run    # the version, the notes and the plan; writes nothing
scripts/release.sh
```

Run it from any branch with a clean tree. It:

1. Works out the next version from the newest `vX.Y.Z` on `main` and the commits on `dev`
   since then, and asks for the bump (major, minor or patch).
2. Creates `release/X.Y.Z` from `origin/dev`, with the `CHANGELOG.md` section committed as
   `chore(release): X.Y.Z`. A `## Unreleased` section at the top of `CHANGELOG.md`, when there
   is one, becomes the section, so you can write the notes by hand.
3. Pushes it and opens two pull requests: `release/X.Y.Z -> main` (the release) and
   `release/X.Y.Z -> dev` (the back-merge).

Every push to `release/X.Y.Z` builds a release candidate `X.Y.Z-rc.N`, which you can
download from that CI run's artifacts. Put a fix for the release on the release branch: it
reaches `dev` with the back-merge.

Merge the release pull request into `main`. The release workflow then:

1. tags `vX.Y.Z`,
2. builds the binaries with GoReleaser and publishes the GitHub release, with the
   `CHANGELOG.md` section as its notes,
3. merges the back-merge pull request into `dev` and deletes `release/X.Y.Z`.

Do not merge the back-merge pull request before the release merges.

Once the tag exists, `go install github.com/JB0t/docgen@vX.Y.Z` (and `@latest`) installs it.

### Major versions

`VERSION_MAJOR` holds the major version being released. Go modules from v2 on need the
major in the module path (`github.com/JB0t/docgen/v2`). A breaking release therefore
starts with its own pull request into `dev`: it changes `go.mod`, the imports and
`VERSION_MAJOR`. `release.sh` refuses to cut a version outside `VERSION_MAJOR`.

### Snapshots

Actions > CI > Run workflow builds a dev snapshot (`<last release>-dev.N+<commit>`) of any
branch as a workflow artifact. It is never published.

## Repository settings

`scripts/setup-github.sh` applies the merge settings, the default branch and the rulesets.
It is safe to re-run after changing it.
