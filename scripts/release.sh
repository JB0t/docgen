#!/usr/bin/env bash
# Cut a release: release/X.Y.Z from origin/dev, with its CHANGELOG section, and
# the two pull requests that finish it.
#
#   scripts/release.sh [--dry-run]
#
# Run it from any branch with a clean working tree. It needs git, gh (logged in)
# and python3.
#
# 1. Works out the next version from the newest vX.Y.Z tag on origin/main within
#    VERSION_MAJOR, and the bump suggested by the commits on origin/dev since
#    then (breaking: major, feat: minor, anything else: patch).
# 2. Creates release/X.Y.Z from origin/dev. When origin/main is not already in
#    it (the merge commit of the previous release), merges origin/main in, so the
#    release merges into main cleanly.
# 3. Commits the CHANGELOG section "chore(release): X.Y.Z". When CHANGELOG.md
#    starts with a "## Unreleased" section, that section is renamed
#    "## vX.Y.Z - <date>" and its text kept; otherwise the section is the commit
#    list.
# 4. Pushes the branch and opens two pull requests:
#      release/X.Y.Z -> main   the release. CI tests it; release-gate lets only
#                              release/* branches merge into main.
#      release/X.Y.Z -> dev    the back-merge, so dev gets the CHANGELOG section.
#
# Every push to release/X.Y.Z builds a release candidate X.Y.Z-rc.N (a workflow
# artifact for testing; never published). A fix for the release goes on the
# release branch, and the back-merge carries it to dev.
#
# Nothing merges here. Merging the pull request into main tags vX.Y.Z, publishes
# the GitHub release with the binaries, merges the back-merge pull request into
# dev and deletes release/X.Y.Z (.github/workflows/release.yml).
#
# --dry-run prints the plan and the release notes, and writes nothing.
set -euo pipefail

DRY_RUN=0
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=1

STABLE=main
SOURCE=dev

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
ask() { local reply; read -r -p "$1 " reply || true; printf '%s' "$reply"; }

# pick <prompt> <default> <option>... - an fzf picker when fzf is installed and
# this is an interactive terminal (default listed first, pre-selected), else a
# plain prompt.
pick() {
  local prompt="$1" default="$2"; shift 2
  if command -v fzf >/dev/null 2>&1 && [[ -t 0 && -t 1 ]]; then
    local choice
    choice="$({ printf '%s\n' "$default"; printf '%s\n' "$@" | grep -vxF -- "$default"; } \
      | fzf --height=~40% --reverse --no-multi --prompt="${prompt} " --header="enter: select, esc: default (${default})")" || true
    printf '%s' "${choice:-$default}"
  else
    local reply; reply="$(ask "${prompt} [${default}]:")"
    printf '%s' "${reply:-$default}"
  fi
}

cd "$(git rev-parse --show-toplevel 2>/dev/null)" || die "not inside a git repository"
[[ -z "$(git status --porcelain)" ]] || die "working tree is not clean - commit or stash first"
git remote get-url origin >/dev/null 2>&1 || die "no 'origin' remote"
command -v python3 >/dev/null 2>&1 || die "python3 is required"
if [[ $DRY_RUN -eq 0 ]]; then
  command -v gh >/dev/null 2>&1 || die "gh (GitHub CLI) is required"
  gh auth status >/dev/null 2>&1 || die "gh is not logged in - run: gh auth login"
fi
git fetch -q origin --tags --prune || die "fetch from origin failed"
git rev-parse -q --verify "origin/${STABLE}" >/dev/null || die "no origin/${STABLE}"
git rev-parse -q --verify "origin/${SOURCE}" >/dev/null || die "no origin/${SOURCE}"

# Always run origin/dev's copy of this script: it is what is being released, and
# a checkout made before a tooling fix carries the old one. Re-runs once.
if [[ -z "${RELEASE_SH_REEXEC:-}" ]]; then
  latest="$(mktemp)"
  if git show "origin/${SOURCE}:scripts/release.sh" > "$latest" 2>/dev/null \
     && ! cmp -s "$latest" scripts/release.sh; then
    echo "This checkout's release.sh differs from origin/${SOURCE}'s - running origin/${SOURCE}'s."
    RELEASE_SH_REEXEC=1 exec bash "$latest" "$@"
  fi
  rm -f "$latest"
fi

series="$(git show "origin/${SOURCE}:VERSION_MAJOR" 2>/dev/null | tr -d '[:space:]' || true)"
[[ "$series" =~ ^[0-9]+$ ]] || die "VERSION_MAJOR is missing or not a number on origin/${SOURCE}"

# Last release: the newest vX.Y.Z tag merged into main within VERSION_MAJOR.
# With none in the series but an older major released, this is the series'
# first release, X.0.0. With no tag at all, the newest "## vX.Y.Z" heading in
# main's CHANGELOG.md counts (releases made before this repository existed).
released="$(git tag --merged "origin/${STABLE}" --list 'v*' \
    | sed -nE 's/^v([0-9]+\.[0-9]+\.[0-9]+)$/\1/p' \
    | sort -t. -k1,1n -k2,2n -k3,3n)"
last="$(grep -E "^${series}\." <<< "$released" | tail -1 || true)"
first_of_series=0
if [[ -z "$last" && -n "$released" ]]; then
  last="$(awk -F. -v s="$series" 'NF == 3 && $1 + 0 < s + 0' <<< "$released" | tail -1)"
  [[ -n "$last" ]] && first_of_series=1
fi
if [[ -z "$last" && -z "$released" ]]; then
  last="$(git show "origin/${STABLE}:CHANGELOG.md" 2>/dev/null \
    | sed -nE 's/^## v([0-9]+\.[0-9]+\.[0-9]+)([[:space:]].*)?$/\1/p' \
    | sort -t. -k1,1n -k2,2n -k3,3n | grep -E "^${series}\." | tail -1 || true)"
fi
[[ -n "$last" ]] || last="${series}.0.0"
IFS=. read -r MA MI PA <<< "$last"

# Hand-written notes: a "## Unreleased" section at the top of dev's CHANGELOG.md.
# When there is one, it becomes the release's section instead of the commit list.
unreleased="$(git show "origin/${SOURCE}:CHANGELOG.md" 2>/dev/null | awk '
  /^## / { if (on) exit; if ($0 ~ /^## Unreleased[[:space:]]*$/) { on = 1; next } exit }
  on
' || true)"
[[ -n "${unreleased//[[:space:]]/}" ]] || unreleased=""

# ── changelog: what dev adds over main ──────────────────────────────────────
breaking=(); feats=(); fixes=(); other=()
while IFS= read -r -d $'\x1e' rec; do
  rec="${rec#$'\n'}"
  [[ -z "${rec//[[:space:]]/}" ]] && continue
  IFS=$'\x1f' read -r -d '' sha subj body <<< "$rec" || true
  # The previous release's own CHANGELOG commit reaches dev by the back-merge.
  [[ "$subj" =~ ^chore\(release\): ]] && continue
  line="- ${subj} (${sha})"
  if [[ "$subj" =~ ^[a-z]+(\([^\)]*\))?!: ]] || grep -q '^BREAKING CHANGE' <<< "$body"; then
    breaking+=("$line")
  elif [[ "$subj" =~ ^feat(\([^\)]*\))?: ]]; then
    feats+=("$line")
  elif [[ "$subj" =~ ^fix(\([^\)]*\))?: ]]; then
    fixes+=("$line")
  else
    other+=("$line")
  fi
done < <(git log --no-merges --format='%x1e%h%x1f%s%x1f%b' "origin/${STABLE}..origin/${SOURCE}"; printf '\x1e')

total=$(( ${#breaking[@]} + ${#feats[@]} + ${#fixes[@]} + ${#other[@]} ))
[[ "$total" -gt 0 || -n "$unreleased" ]] || die "no commits on origin/${SOURCE} beyond origin/${STABLE} - nothing to release"

un_breaking=0; un_feats=0
if [[ -n "$unreleased" ]]; then
  grep -qE '^###[[:space:]]+Breaking|BREAKING|^-[[:space:]]+[a-z]+(\([^)]*\))?!:' <<< "$unreleased" && un_breaking=1
  grep -qE '^###[[:space:]]+Features|^-[[:space:]]+feat(\([^)]*\))?:' <<< "$unreleased" && un_feats=1
fi

if [[ $first_of_series -eq 1 ]]; then suggest="major"
elif [[ ${#breaking[@]} -gt 0 || $un_breaking -eq 1 ]]; then suggest="major"
elif [[ ${#feats[@]} -gt 0 || $un_feats -eq 1 ]]; then suggest="minor"
else suggest="patch"; fi

echo "Source: origin/${SOURCE}   Target: origin/${STABLE}   Series: ${series}.x   Last release: ${last}   Commits: ${total}   Suggested bump: ${suggest}"
[[ $first_of_series -eq 1 ]] && echo "No ${series}.x release yet: this is the first, ${series}.0.0 (a major bump from ${last})."

bump="$(pick "Bump:" "$suggest" major minor patch)"
bump="${bump:-$suggest}"
case "$bump" in
  major) new="$((MA + 1)).0.0" ;;
  minor) new="${MA}.$((MI + 1)).0" ;;
  patch) new="${MA}.${MI}.$((PA + 1))" ;;
  *) die "bump must be major, minor or patch" ;;
esac
[[ "${new%%.*}" == "$series" ]] \
  || die "v${new} is outside the ${series}.x series. A Go module's v2+ also needs the /v${new%%.*} module path suffix: change go.mod, the imports and VERSION_MAJOR first, in their own pull request"

rel_branch="release/${new}"
git rev-parse -q --verify "origin/${rel_branch}" >/dev/null && die "${rel_branch} already exists on origin"
git rev-parse -q --verify "refs/tags/v${new}" >/dev/null && die "v${new} already exists"

# ── release notes ───────────────────────────────────────────────────────────
notes="$(mktemp)"
heading="## v${new} - $(date -u +%Y-%m-%d)"
if [[ -n "$unreleased" ]]; then
  { echo "$heading"; echo; awk 'NF { on = 1 } on' <<< "$unreleased"; echo; } > "$notes"
else {
  echo "$heading"
  echo
  for group in breaking feats fixes other; do
    declare -n items="$group"
    [[ ${#items[@]} -eq 0 ]] && continue
    case "$group" in
      breaking) title="Breaking" ;; feats) title="Features" ;;
      fixes) title="Fixes" ;; other) title="Other" ;;
    esac
    echo "### ${title}"
    printf '%s\n' "${items[@]}"
    echo
  done
} > "$notes"
fi

main_merged=1
git merge-base --is-ancestor "origin/${STABLE}" "origin/${SOURCE}" || main_merged=0

echo
echo "────────── release notes ──────────"
cat "$notes"
echo "───────────────────────────────────"
echo
echo "Plan:"
echo "  - create ${rel_branch} from origin/${SOURCE} (${total} commit(s))"
[[ $main_merged -eq 0 ]] && echo "  - merge origin/${STABLE} into it (the previous release's merge commit)"
if [[ -n "$unreleased" ]]; then
  echo "  - rename CHANGELOG.md's '## Unreleased' to '${heading}', commit 'chore(release): ${new}'"
else
  echo "  - prepend the CHANGELOG.md section for v${new}, commit 'chore(release): ${new}'"
fi
echo "  - push ${rel_branch}; every push to it builds a release candidate ${new}-rc.N"
echo "  - open the pull requests ${rel_branch} -> ${STABLE} (the release) and ${rel_branch} -> ${SOURCE} (the back-merge)"
echo "  - merging the first tags v${new} and publishes the release; the workflow then merges the back-merge"

if [[ $DRY_RUN -eq 1 ]]; then
  echo
  echo "dry run: nothing written. Notes at ${notes}"
  exit 0
fi

go="$(ask "Create ${rel_branch} and open the pull requests? [y/N]:")"
[[ "$go" =~ ^[Yy]$ ]] || { echo "aborted"; rm -f "$notes"; exit 0; }

start="$(git symbolic-ref -q --short HEAD || git rev-parse HEAD)"
restore() { git checkout -q "$start" 2>/dev/null || true; }
trap restore EXIT

git checkout -q -b "$rel_branch" "origin/${SOURCE}"
if [[ $main_merged -eq 0 ]]; then
  git merge -q --no-ff --no-edit -m "Merge ${STABLE} into ${rel_branch}" "origin/${STABLE}" \
    || die "merging origin/${STABLE} into ${rel_branch} conflicts - resolve it on a branch into ${SOURCE} first, then re-run (git merge --abort; git checkout ${start}; git branch -D ${rel_branch})"
fi
python3 - "$new" "$notes" "${unreleased:+unreleased}" <<'PY'
import re, sys, pathlib
version, notes_path, mode = sys.argv[1], sys.argv[2], sys.argv[3]
section = pathlib.Path(notes_path).read_text().rstrip() + "\n"
path = pathlib.Path("CHANGELOG.md")
head = "# Changelog\n\n"
body = path.read_text() if path.exists() else head
if mode == "unreleased":
    new = re.sub(r"(?m)^## Unreleased[ \t]*$", section.split("\n", 1)[0], body, count=1)
elif body.startswith("# Changelog"):
    # Keep any intro text between the title and the first release section.
    title, rest = body.split("\n", 1)
    m = re.search(r"(?m)^## ", rest)
    intro, releases = (rest[:m.start()], rest[m.start():]) if m else (rest, "")
    intro = intro.strip("\n")
    new = head + (intro + "\n\n" if intro else "") + section + ("\n" + releases if releases else "")
else:
    new = head + section + "\n" + body
path.write_text(new)
PY
git add CHANGELOG.md
git commit -q -m "chore(release): ${new}"
git push -q -u origin "$rel_branch" || die "push of ${rel_branch} failed"

body_main="$(mktemp)"
{
  echo "Release **v${new}**, cut by \`scripts/release.sh\` from \`${SOURCE}\`."
  echo
  echo "Merging this (merge commit only) tags \`v${new}\`, publishes the GitHub release with the binaries, merges the back-merge pull request into \`${SOURCE}\` and deletes \`${rel_branch}\`."
  echo
  echo "Every push to \`${rel_branch}\` builds a release candidate \`${new}-rc.N\` (CI run artifacts)."
  echo
  sed '1s/^## /### /' "$notes"
} > "$body_main"
pr_main="$(gh pr create --base "$STABLE" --head "$rel_branch" --title "Release v${new}" --body-file "$body_main")" \
  || die "pushed ${rel_branch}, but opening its pull request into ${STABLE} failed"
pr_dev="$(gh pr create --base "$SOURCE" --head "$rel_branch" --title "Back-merge release v${new} into ${SOURCE}" \
  --body "Brings the v${new} CHANGELOG section (and any fix made on \`${rel_branch}\`) back into \`${SOURCE}\`. The release workflow merges this once ${pr_main} has merged; do not merge it before then.")" \
  || die "opened ${pr_main}, but the back-merge pull request into ${SOURCE} failed - open it by hand"
rm -f "$notes" "$body_main"

echo
echo "Pushed ${rel_branch}."
echo "  Release:    ${pr_main}"
echo "  Back-merge: ${pr_dev}"
echo "Each push to ${rel_branch} builds release candidate ${new}-rc.N (CI run artifacts)."
echo "Merging the release pull request tags v${new} and publishes it."
