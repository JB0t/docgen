# docgen

[![CI](https://github.com/JB0t/docgen/actions/workflows/ci.yml/badge.svg?branch=dev)](https://github.com/JB0t/docgen/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/JB0t/docgen)](https://github.com/JB0t/docgen/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/JB0t/docgen.svg)](https://pkg.go.dev/github.com/JB0t/docgen)

Documentation tooling that starts from facts, not guesses. `docgen facts` extracts
what the code actually contains, without any AI. `docgen check` validates docs
against those facts. `docgen draft` has a local model write module overviews from the
facts and code, checks every claim the same way, and can add an exact reference list
built from the facts alone. `docgen ask` answers questions about the code from the facts, those
overviews and the source, checked the same way; `docgen code` makes small, well-scoped
code changes as a reviewed patch. `docgen gen` writes new boilerplate (playbooks,
workflows, pipelines, Terraform, charts) from retrieved specs of the modules and
actions it uses, and checks it with the real tools before anything is written.

## Install

A release binary for your platform is on the
[releases page](https://github.com/JB0t/docgen/releases) (Linux, macOS and Windows; amd64
and arm64), with `checksums.txt` next to it. With Go installed:

```bash
go install github.com/JB0t/docgen@latest
# or, from a checkout: build with the release version stamped, and install completion
scripts/install.sh              # optional arg: install dir (default ~/.local/bin)
```

`scripts/install.sh` stamps the release version (see Versions) into `docgen --version` and
installs tab completion for bash (and zsh or fish when present). The companion scripts
(`llm-server`, `llm-fit`, `diffgen`, `docgen-cron`, `docgen-job`) are in
[`contrib/`](contrib/README.md); put it on your `PATH` to use them.

### Versions

Releases are git tags `vX.Y.Z` (semver) with a
[GitHub release](https://github.com/JB0t/docgen/releases) each; `CHANGELOG.md` lists what
changed. `docgen --version` (or `-v`) prints the version it was built from:

- `v1.2.0`: a release binary, `go install ...@v1.2.0`, or `scripts/install.sh` at that tag.
- `v1.2.0-3-gabc1234`: `scripts/install.sh` three commits after it (`-dirty` with
  uncommitted changes).
- `v1.2.0-rc.1`: a release candidate, built by CI on a `release/1.2.0` branch.
- `dev-abc1234`: `scripts/install.sh` before the first tag.
- `v0.0.0-20261016080119-1fe5fe1ad6fd`: a plain `go build` from a checkout; Go's
  pseudo-version, the commit's time and hash (`+dirty` with uncommitted changes).

How changes, releases and branches work is in [CONTRIBUTING.md](CONTRIBUTING.md).

## Walkthrough: trying it on a repository

```bash
cd ~/git/some-repo
git switch -c docgen-trial          # draft writes files; keep them on a branch

# 1. What can it discover with no help at all?
docgen facts --summary              # languages, symbols, doc-comment coverage
docgen check --coverage             # problems in the existing docs, plus gaps
```

Without configuration, step 1 checks repo paths, code identifiers, environment
variables and generated-section markers. It does not check the CLI's commands and
flags, because docgen does not yet work out on its own which program is the CLI or how
to run it. Tell it in `docgen.yaml` at the repository root:

```yaml
clis:
  - name: mytool                    # the command the docs use
    run: [./mytool]                 # how to run it from dir: [go, run, .], [venv/bin/python, cli.py], ...
    dir: cmd/mytool                 # its source directory, relative to the repo root (or absolute)
    doc: USAGE.md                   # where draft --cli writes; keeps a hand-written README untouched
    env: {}                         # only if some commands appear only with certain settings
```

```bash
# 2. Same checks, now including every command and flag in the docs
docgen facts --summary              # now lists the CLI and its command count
docgen check --coverage
```

The errors from step 2 are the places where the docs are wrong: commands that do not
exist, flags a command does not have, arguments it does not take, impossible values,
and paths that are not there. Fix them by hand; draft never edits text outside its
markers.

```bash
# 3. Generated docs (needs the model: about 20 GB of RAM while running, 1-2 min per module)
docgen modules                      # what --all would cover
docgen draft some/module --dry-run  # look at one before committing to all
docgen draft --all                  # modules and the CLI usage section
docgen check                        # everything generated must pass
git diff                            # review
```

Run `draft` from the repository root; `facts` and `check` also take a path.

### Reading the output

- Findings are `file:line: severity: message`. **Errors** are definitely wrong;
  **warnings** are probably wrong or refer to something outside the code (add it to
  `ignore:` in `docgen.yaml` once you have decided it is fine); **info** lines from
  `--coverage` are things no doc mentions.
- `check` exits 1 when there are errors (2 if docgen itself failed), so
  `docgen check && echo clean` works in scripts and hooks.
- `docgen facts | less` shows everything it discovered, including each CLI command's
  scraped help text; `docgen check --json | jq` helps with long reports.
- `draft` starts and stops the model server itself; Ctrl+C is safe and keeps modules
  already written. `llm-server status` and `llm-server log` show what the server is
  doing.

## Facts

```bash
docgen facts --summary          # counts per language and symbol kind, doc-comment coverage
docgen facts -o facts.json      # the full fact sheet
docgen facts ../other-repo
```

The fact sheet lists files (language, lines, hash), symbols, environment variables
read by the code, Markdown docs with their headings, and the command tree of every CLI
listed in `docgen.yaml` (scraped from `--help`, three levels deep; cobra, argparse
and click layouts are understood).

| Language | Extracted | How |
|---|---|---|
| Go | packages, functions, methods, types, constants, variables, doc comments | Go parser |
| Python | modules, classes, methods, functions, signatures, docstrings | Python's `ast` (regex fallback without `python3`) |
| Terraform | variables (type, default, description), outputs, resources, data, modules, providers, locals; `.tfvars` values; Terragrunt dependencies and inputs; test runs (`.tftest.hcl`) | HCL parser |
| Bash | scripts (header comment), functions, flags handled in `case` | patterns |
| C# | namespaces, types, public methods and constructors, `///` docs | patterns |
| Rust | modules (`//!`), items, `impl` methods, `///` docs | patterns |
| R | functions, roxygen `#'` docs and `@export` | patterns |
| Helm | chart name, version, description; every `values.yaml` key with its comment | YAML parser |
| Ansible | plays, tasks, role defaults and vars with comments | YAML parser |
| YAML | GitHub workflow names, jobs and inputs; Compose services; Kubernetes kinds | YAML parser |

Environment variables are found per language (`os.Getenv`, `os.environ`, `${VAR:-}`,
`Environment.GetEnvironmentVariable`, `env::var`, `Sys.getenv`, Ansible `lookup('env')`).

## Check

```bash
docgen check                # exits 1 on errors: use in CI or a git hook
docgen check --strict       # ... and on warnings
docgen check --coverage     # also list CLI commands and env vars no doc mentions
docgen check --json
docgen check --fix          # opt-in: the model corrects wrong claims in hand-written text, marked
```

| Finding | Severity |
|---|---|
| CLI subcommand or flag in a doc that does not exist (`tool sub --flag` in code blocks or backticks; `tool group a \| b \| c` lists are understood) | error |
| Repo path that does not exist | error (warning when it may be runtime output) |
| Unclosed or orphaned `docgen:begin`/`docgen:end` marker | error |
| Generated section whose source files changed since generation | warning |
| Function, symbol, env var or flag in backticks that appears nowhere in the code | warning |
| CLI command or env var no doc mentions (`--coverage`) | info |

What is right without being in a help page or a committed file is not reported:

- **Commands the help pages do not list.** A cobra tool is asked about any other
  subcommand a doc uses (`tool <path> --help`), so hidden commands and deprecated names
  pass, and so do group action flags such as `clctl t -l` for `clctl tasks list`.
- **Environment variables built from parts.** `${prefix}THREADS` with
  `prefix=LLM_DIGEST_` reads `LLM_DIGEST_THREADS`.
- **Paths that exist outside the repository.** Paths under `.git/`, paths git ignores
  (local configs, token caches), files the code names (`"status.md"`), and file types
  such as `.tfvars`.

### Fixing hand-written text: `check --fix`

```bash
docgen check --fix              # print a patch per doc, for review
docgen check --fix --apply      # apply them; review with git diff
```

`check` never changes a file. `--fix` is the one way docgen edits text a person wrote,
and it is opt-in on every run: for each doc with wrong commands, flags, paths,
identifiers or env vars outside generated sections, the local model corrects just those
lines through the same engine as `docgen code` (search/replace blocks, path check,
`git apply --check`), then the result is checked again; a change that leaves a finding
or adds a new one is rejected and asked for once more. Every edited region starts with

```markdown
<!-- docgen:edited 2026-10-09: replaced tasks list-timers with timers list -->
```

so the edit is loud in the file and in `git diff`; delete the comment once you have
read the change. Stale generated sections are not `--fix`'s business: `docgen draft`
redrafts those. `docgen-cron` (below) never passes `--fix`.

Generated sections look like this; `scope` is the file or directory they document and
`hash` its content hash at generation time:

```markdown
<!-- docgen:begin id=internal/notes:overview scope=internal/notes hash=3f2a9c1b7d04 -->
...
<!-- docgen:end id=internal/notes:overview -->
```

## Draft

```bash
docgen modules                                     # what --all would cover: new, not listed, stale, up to date
docgen draft internal/notes --dry-run              # print the updated README
docgen draft internal/notes internal/cache
docgen draft --cli clctl                           # usage section for a CLI from docgen.yaml
docgen draft --all                                 # every module and CLI (or --all <dir>)
```

The model writes caveman-style prose: terse sentences without articles or filler, with
every name, path, command and flag exact. Sections are only redrafted when their source
changes; `docgen draft --all --force` rewrites existing ones in this style.

`draft` starts the model server only when a module actually needs it (`llm.start`,
default `llm-server start`) and stops it when done or on Ctrl+C (`llm.stop`), so the
model only holds its memory while drafting. If the server is already running, it is
left running. Set `llm.start: none` to manage it yourself.

For each module directory, `draft` writes sections between markers in
`<module>/README.md` (or `--doc`):

- **Overview**: one to three paragraphs drafted by the model from the module's facts
  (files, exported symbols with signatures and doc comments, environment variables) and
  as much source as `llm.max_source_toks` allows. It is checked like any doc. If it
  names something the code does not contain, it is sent back once with the problems
  listed, and left out if it still fails.
- **Reference**, only when `docgen.yaml` sets `draft.reference: true`: every exported
  symbol per file with its first doc-comment line, built directly from the facts. No
  model is involved, so it is always exact. It is off by default because godoc and the
  source already show it and every change to the module makes it stale. With it off,
  an existing reference section is removed the next time its module is redrafted, and an
  overview that fails its checks leaves the doc as it was.

For a CLI listed in `docgen.yaml`, `--cli NAME` writes a **usage** section (to the
CLI's `doc`, default `<dir>/README.md`): a table of every command with its description
and own flags, built from the scraped `--help` output, and model-written examples for
each command. Every example is checked against the command tree: the subcommand must
exist, every flag must belong to it, value-taking flags must have a value, and commands
whose usage line shows no arguments must not be given any. A command whose examples
fail is retried once, then left out, without losing the others. When the `--help` scrape
finds no commands (check the CLI's `run`, `dir` and `env`), no usage section is written.

`--all` keeps every existing generated section current, but starts generated sections in
a module that has none yet only when `draft.new_docs` lists the module's directory or
one above it, so an unattended run does not add a README to every package. `docgen
modules` shows the others as `not listed`; `--new-docs` drafts them for one run, and
naming a module directory on the command line always drafts it. CLIs listed under
`clis:` are always covered.

Text outside the markers is never touched. Modules whose sources have not changed
since their sections were written are skipped (`--force` to redo). Test files are left
out of both sections.

### Model server

**First time on a machine: `contrib/llm-fit`.** It discovers the CPU, RAM, GPU, disk and the
`llama-server` binary, picks for each preset (code, digest, embed) the largest model and
context that fit safely (weights + KV cache + headroom against free VRAM, or RAM minus a
reserve), verifies the model tags on Docker Hub, prints the plan with the matching
`docgen.yaml` `llm:` values, and after a prompt writes it to
`~/.config/llm-server/env`, which `llm-server` sources on every run. `--download` then
asks, per model, before fetching anything; `--dry-run` only shows the plan. Rerun it when
the hardware changes; `LLM_NO_FIT=1` ignores the file.

`contrib/llm-server start|stop|status|log` runs llama.cpp's `llama-server` on
`127.0.0.1:8081` with an OpenAI-compatible API, by default with Qwen3-Coder-30B-A3B
(a mixture-of-experts model: 30B parameters, about 3B active per token, so it runs at
usable speed on CPU). It needs about 20 GB of RAM. By default it runs gently: 10 generation and 12 prompt
threads at the lowest CPU and I/O priority, so a long batch cannot starve the machine;
`LLM_PROFILE=fast llm-server start` uses every core (about 25% quicker, much hotter).
Thread counts, context size, port and model file are environment variables documented
at the top of the script. Any
OpenAI-compatible server works; point `llm.url` at it.

**A remote server (Ollama, vLLM, a shared llama-server):** set `DOCGEN_LLM_URL` and
`DOCGEN_LLM_MODEL` (see [Configuration](#configuration)); docgen then neither starts nor
stops it. With Ollama, `/v1/models` is the readiness check and the model name must be the
Ollama tag (`ollama list`). Ollama has no `/props`, so docgen cannot check a prompt
against the context size: give the model a `num_ctx` that holds `max_source_toks` +
`max_response_toks` + about 2000 tokens of prompt text, because Ollama silently truncates a
prompt that does not fit instead of refusing it.

### Code changes

`docgen code` asks the configured model for a change to source files in the requested
scope. It prints the patch for review by default; `--apply` applies it only after
`git apply --check` succeeds. It does not run model-generated code or tests. Excluded
paths and common secret-file names are omitted.

```bash
docgen code --request "Move timer commands from tasks to clctl timers list"
docgen code --request "Add a --json flag" cmd/timers.go
docgen code --format blocks --apply --request "Rename the command" cmd/timers.go
git diff
```

**Scope.** Without files or directories, docgen picks the files the request most likely
concerns, without the model: the request's words (identifiers split into their parts,
filler dropped) are matched against paths, symbol names and doc comments from the facts
and against contents with `git grep`, and files are taken in rank order until
`llm.max_source_toks` is spent. It prints the words it searched for and the files it
chose. Give a scope to control it exactly; `.` is the whole repository, which rarely fits.

**Budget.** `llm.max_source_toks` and `llm.max_response_toks` are tokens, estimated at
3.2 characters each (slightly pessimistic for code). Before asking, docgen checks that
the prompt plus the answer fit the server's context (llama-server reports it on
`/props`) and refuses otherwise. A prompt is never silently cut.

**Format.** `--format diff` (default) asks for a unified diff. Before checking it,
docgen repairs the mistakes that make git call a model's diff a "corrupt patch": hunk
headers whose line counts do not match the hunk, blank context lines written without
their leading space, and text after the last hunk. `--format blocks` asks instead for
search/replace blocks (the file path, then `<<<<<<< SEARCH` / `=======` /
`>>>>>>> REPLACE`): the model copies the lines to replace and writes the new ones, with
no line counting, which small models get right more often. docgen applies the blocks to
copies of the files (exact match, then ignoring trailing whitespace, then ignoring
indentation) and makes the diff itself, so both formats end in the same checks: every
changed file inside the scope, `git apply --check`, then `--ignore-whitespace` as a
last resort with a warning.

**Retry.** A patch git rejects, or an answer in the wrong shape, is sent back to the
model once with the exact error, then given up on. The raw answer and the cleaned patch
are saved to `.git/docgen/answer.txt` and `.git/docgen/patch.diff` (overwritten by each
run), and the error names them.

Answers are streamed, so on a terminal one status line follows the model: prompt
processing (`reading prompt: 5120/11300 tokens (45%)`), then the answer
(`writing: 312 tokens, 40 lines, 9.8 tok/s`). Nothing is drawn when stderr is not a
terminal.

`contrib/diffgen` wraps this: it finds the files with `git grep`, shows them with a size
estimate, and runs `docgen code` from the repository root, so paths can be given
relative to any directory or as absolute paths:

```bash
diffgen -s list-timers -d cmd -r "Move 'tasks list-timers' to 'timers list'"
diffgen -r @request.md --apply cmd/timers.go
diffgen -f blocks -r "Add a --json flag" cmd/timers.go
```

## Ask

```bash
docgen ask "How does clctl decide which timers are running?"
docgen ask "What does the digest command cache, and where?" cmd/digest.go
```

`docgen ask` answers a question about the repository with the local model. Its prompt
holds three layers, cheapest first: the facts for the files concerned (exported symbols
with their doc comments and line numbers), the overviews `docgen draft` already wrote
for those modules, and as much of their source as `llm.max_source_toks` allows. Files
are picked from the question's words exactly as `docgen code` picks them, or given as a
scope.

The answer is checked like any document: identifiers, paths, commands or flags the code
does not contain are sent back to the model once with the list. Anything still wrong is
printed under the answer as "Not found in the code", so a wrong claim is visible rather
than silently trusted. Overviews are the model's cached understanding of each module:
the more of them exist (`docgen draft --all`), the less source has to be re-read per
question and the better the answers, so drafting is worth running as a routine: stale
overviews are redrafted, current ones skipped, and `draft` starts and stops the model
server itself. The same idea as clctl's nightly digest:

```cron
# ~/.local/bin on PATH for docgen and llm-server; the trial branch keeps generated text reviewable
15 2 * * * cd ~/git/some-repo && git switch -q docgen-trial && docgen draft --all >> ~/.local/state/docgen-nightly.log 2>&1
```

## Generate: `docgen gen`

`docgen gen KIND` writes the first version of new files: a playbook or role, a GitHub
Actions workflow or GitLab pipeline, a Terraform module, a Helm chart. It is for the
generic part of starting something: you know the tool and roughly what you want, the
model writes the boilerplate, docgen checks it, and you finish it. KIND is `ansible`,
`github-actions` (`gha`), `gitlab-ci` (`gitlab`), `terraform` (`tf`), `helm` or `any`.

What makes the output usable is what goes in before the model and what is checked
after it:

- **Specs.** Small models know a tool's syntax but guess its parameter names. docgen
  keeps the exact interface of what you use (module options from `ansible-doc`, resource
  schemas from Terraform, action inputs from `action.yml`) and puts the entries a
  request needs into the prompt. The same specs check the answer.
- **Your conventions.** `--example` files (or `gen.examples` in `docgen.yaml`) show the
  model how your project names, lays out and comments things.
- **What it builds on.** `--context` files are shown read-only. The values they need from
  a caller (playbook variables they use without defining, Terraform variables without a
  default) are listed for the model, and a generated workflow that does not pass one
  fails its check.
- **Checks before anything is written.** The answer is applied in memory and checked in
  a throwaway copy of the tree, where every directory on the way to a generated file is
  real and everything else is a symlink. Validators therefore see your roles,
  `ansible.cfg`, `.terraform` and charts, and nothing real changes. Failures go back to
  the model, which fixes the current files with search/replace blocks, up to
  `gen.retries` (2) times. Linter style rules are reported as advice and are not sent back.

### Worked example: AKS node pools, then a workflow that runs the playbook

The commands below are the whole flow. Steps 1 and 2 happen once per tool and project.

**1. Specs for the tools you will use.** The collection must be installed for
`ansible-doc` to read it (it has to be anyway, to run the playbook):

```bash
ansible-galaxy collection install azure.azcollection
docgen spec add ansible azure.azcollection          # 435 modules, about 2 minutes with embeddings
docgen spec add action actions/checkout@v4 azure/login@v2
docgen spec list
```

**2. The target.** For a single playbook, nothing; `--out` names the file. For a role
or a chart, let the tool's own generator make the skeleton, and gen fills it in:
`ansible-galaxy role init --init-path roles aks_nodepool` (or `--scaffold` on the gen
command below, which runs it when the folder does not exist), `helm create charts/web`.

**3. Check what will be retrieved,** before spending model time:

```bash
docgen spec search "provision aks node pools in a resource group" --kind ansible
docgen gen ansible --plan --example playbooks/resource_group.yml \
    --out playbooks/aks_nodepools.yml --request @aks-request.md
```

`--plan` prints the specs it picked (with their scores), the examples and context
files, the token count, and with `--prompt` the full prompt. If the search missed a
module, name it with `--spec azure_rm_aksagentpool`. If it picked noise, lower
`--max-specs` (0 means only `--spec`).

**4. Generate.**

```bash
docgen gen ansible --apply --example playbooks/resource_group.yml \
    --out playbooks/aks_nodepools.yml --request @aks-request.md
```

where `aks-request.md` says what you want, for example "Write a playbook that provisions
AKS node pools in an existing cluster in a resource group. The pools are a list
variable (name, vm_size, count, mode, optional autoscaling min/max, labels, taints)."
Without `--apply` you get the diff (saved to `.git/docgen/gen.diff`) and nothing is
written. With it the files are written only when the checks pass; `--force` writes them
anyway. docgen starts the model server when it is down and stops it afterwards. Loading
the 30B model takes a few minutes, so for a session of several commands run
`llm-server start` first: docgen leaves alone a server it did not start. Expect minutes,
not seconds: on a laptop CPU (gentle profile) the model writes about 3
tokens a second, so an 80-line playbook takes about 5 minutes per round, and each fix
round costs roughly the same again.

**5. The follow-on: a workflow that runs it.** Give the playbook as context. docgen
works out which variables it needs (in the test run `resource_group`, `cluster_name`
and `nodepools`), tells the model, and fails the workflow if one is never passed:

```bash
docgen gen github-actions --apply --context playbooks/aks_nodepools.yml \
    --out .github/workflows/aks-nodepools.yml \
    --request "A workflow_dispatch workflow that takes the playbook's variables as inputs, logs in to Azure with OIDC and runs the playbook"
```

The GitLab version is the same with `gitlab-ci` and `--out .gitlab-ci.yml`.

**6. Finish it, and check your edits the same way:**

```bash
docgen validate ansible playbooks/aks_nodepools.yml
docgen validate github-actions .github/workflows/aks-nodepools.yml --context playbooks/aks_nodepools.yml
```

`validate` runs the same checks without the model and exits 1 when one fails.
`TODO` comments mark what the model could not settle from the specs.

### Specs

```bash
docgen spec add ansible COLLECTION|MODULE... [--only 'azure_rm_aks*']
docgen spec add terraform DIR|schema.json [--only 'azurerm_kubernetes_*']
docgen spec add action OWNER/REPO@REF...
docgen spec add docs NAME [PATH...] [--cmd 'helm show values bitnami/nginx'] [--for helm]
docgen spec list [SET] | show NAME | search QUERY [--kind KIND] | rm SET | embed
```

- **ansible.** A collection (all modules, or those matching `--only`) or single modules,
  read with `ansible-doc -j`. Options shared by nearly every module of a collection
  (Azure's authentication settings) are listed once per prompt instead of in every module.
- **terraform.** Every resource and data source of the providers in a directory where
  `terraform init` has run, through `terraform providers schema -json`, or a saved
  schema file. Use `--only` for big providers: azurerm has over a thousand types.
- **action.** `action.yml` fetched from GitHub: inputs (required unless they have a
  default) and outputs.
- **docs.** Markdown or text files and folders, cut at headings, and a command's output
  with `--cmd`: `helm show values`, `kubectl explain --recursive`, a team style guide.
  `--for KIND` limits them to one kind. This is the way to bring in sources docgen
  has no parser for, such as a chart's values or a GitLab CI reference page saved as Markdown.

Specs are per user, not per repository: `$DOCGEN_SPECS`, default
`~/.local/share/docgen/specs`, one JSON file per set. Adding a set again refreshes it
and keeps the embeddings of entries that did not change.

Retrieval scores every entry by keywords (the request's words against names, summaries
and option names, so "node pools" finds `azure_rm_aksagentpool`) and, when
`llm-server`'s embed preset is available, by meaning. docgen starts that server
itself, as it does the code model; without it, search is by keywords only and says so.
Read-only modules (`_info`, `_facts`) and data sources rank lower unless the request
asks to read something. Picked specs stay within `gen.spec_toks` (8000), and an entry
that does not fit in full is shown without descriptions.

### Checks

Each kind runs its built-in checks plus the tools it finds on `PATH`. A missing tool is
listed as skipped, not failed.

- **All kinds:** YAML, JSON and HCL syntax (Helm templates excepted).
- **ansible:** every task's module exists in the stored collection (`azure_rm_aksnodepool`
  gets "did you mean azure_rm_aksagentpool"). Every option exists, suboptions included,
  with aliases and the collection's common options allowed. Required options are present
  and literal values are among the choices. Then `ansible-playbook --syntax-check` on
  playbooks, and on a wrapper playbook for roles and task files, run from the directory
  holding `ansible.cfg`. `ansible-lint` findings are advice.
- **github-actions:** `on:` and `jobs:` are present, and every job has `runs-on` or `uses`.
  Every `${{ inputs.X }}` is declared, and `with:` of each step matches the stored action's
  inputs, required ones included. The context's values are passed. No `run:` script
  expands `${{ inputs.* }}` or `${{ github.event.* }}` directly (script injection: they
  belong in the step's `env:`). A job that logs in with OIDC (`azure/login`,
  `aws-actions/configure-aws-credentials` or `google-github-actions/auth` without a
  stored secret) has `permissions: id-token: write`, and a `permissions:` block that
  leaves out `contents: read` fails when the job uses `actions/checkout`. A variable the
  context playbook loops over must not be passed as `-e name=value`, which makes a
  string; it has to go in as JSON. Then `actionlint`, whose shellcheck findings are advice.
- **gitlab-ci:** every job has a script, stage names are declared, and `needs`, `extends`
  and `dependencies` name jobs in the file. Every `$[[ inputs.X ]]` is declared in the
  `spec:` header, and the context's values are passed.
- **terraform:** resource and data types exist in the stored schema. Arguments and nested
  blocks exist and are writable (setting a computed-only attribute fails), required
  ones are present, and dynamic blocks are checked. This works without `terraform init`.
  Then `terraform fmt` rewrites the files, and `terraform validate` runs where `.terraform`
  exists.
- **helm:** `helm lint` and `helm template`, piped through `kubeconform` when installed.
- **Your own:** `gen.validate.<kind>` commands in `docgen.yaml`, run in the stage with
  `{files}` and `{dir}` filled in. Their output on failure goes back to the model.

gen does not run the generated code, and it never writes outside `--out` (default: the
current directory). Specs make wrong names unlikely; they do not make the logic right.
Review the result like a colleague's first draft.

## Keeping docs current: `docgen-cron`

`contrib/docgen-cron` runs `docgen draft --all` and `docgen check` in each repository
listed in `~/.config/docgen/repos` (one path per line, `~` and `#` comments allowed), or
given as arguments. A repository is skipped when nothing changed since the last run
(HEAD plus the content of every tracked or untracked file), so an hourly schedule costs
nothing on quiet days and a daily one catches up after a busy one. It only ever
redrafts generated sections; hand-written text is never edited unattended (`check --fix`
is a by-hand command). Reports go to `~/.local/state/docgen/<repo>.log` and
`summary.log`.

```bash
printf '%s\n' ~/git/some-repo ~/git/other-repo > ~/.config/docgen/repos
docgen-cron --dry-run                 # which repos would run
docgen-cron                           # run now
docgen-cron --install-cron            # prints the crontab line; add it with crontab -e
docgen-cron --install-cron "0 * * * *"   # hourly instead of 02:15 daily
```

With `--commit`, a repository whose working tree was clean before the run gets one
commit, `docs: docgen draft (generated sections, DATE)`, on its current branch; a tree
with your own uncommitted work is left as it is, changes included, for you to review.
The model server is started and stopped by docgen; if `llm-server` is busy with clctl's
digest model the run fails for that repo and is retried next time.

### In a cluster: `docgen-job`

`contrib/docgen-job` is the entrypoint of the nightly Kubernetes CronJob (image
`golang:1.27`, nothing installed). It builds docgen from this repository
(`DOCGEN_REF`, default `main`: the latest release) with `scripts/install.sh`, then for each entry
of `DOCGEN_REPOS` (`gitlab:<group/path>` or `github:<owner/repo>`, optionally
`@branch`) clones the repository fresh (depth 1: draft and check never read history),
runs `docgen draft --all` and `docgen check` against the model server in
`DOCGEN_LLM_URL`/`DOCGEN_LLM_MODEL`, and when draft changed something commits as
`docgen <docgen@localhost>`, pushes `docgen/<UTC date>` and opens an MR or PR whose
description holds the check counts and at most 20 findings, only those in files the
draft changed; the full report stays in the job log. It never pushes to a default branch and never runs
`check --fix`. A repository without `docgen.yaml` is skipped; one that fails does not
stop the others, and the exit status is 1 if any failed. An open docgen MR/PR from an
earlier night is left alone when it already holds the same files, else closed in favour
of the new one (kept open, with a comment, if people committed to it). The full
environment contract is in the script's header (`docgen-job --help`);
`docgen-job --dry-run` prints the plan, and `contrib/tests/docgen-job-test` tests it with
local repositories and fake APIs.

## Configuration

`docgen.yaml` in the repository root; everything is optional.

```yaml
exclude: [TODO.md, "legacy/**"]     # added to the defaults (.git, node_modules, venvs, dist, ...)
docs: ["**/*.md"]                   # which files are documentation
clis:
  - name: clctl                     # how docs invoke it
    run: [go, run, .]               # how to run it (go run tools are built once)
    dir: .                          # repo-relative; "." for a CLI at the repository root
    doc: USAGE.md                   # where draft --cli writes (default <dir>/README.md)
    env:                            # set while scraping --help
      NOTES_DIR: ~/notes
ignore:                             # references never to report
  - SOME_EXTERNAL_NAME
llm:                                # for draft, code, ask and gen; these are the defaults
  url: http://127.0.0.1:8081/v1
  model: qwen3-coder
  max_source_toks: 24000            # tokens of source per prompt (3.2 characters each)
  max_response_toks: 6000           # tokens the answer may take
  start: llm-server start           # run when the server is down ("none" to disable)
  stop: llm-server stop             # run afterwards, only if docgen started it
  embed_url: http://127.0.0.1:8083/v1   # spec search by meaning ("none": keywords only)
  embed_model: nomic-embed-text
  embed_start: llm-server start embed
  embed_stop: llm-server stop embed
draft:                              # for docgen draft
  new_docs: [deploy, internal/collect]  # where --all may start docs in a module (default: nowhere)
  reference: false                  # add the exact reference list to module READMEs (default off)
gen:                                # for docgen gen and validate; numbers are the defaults
  spec_toks: 8000                   # tokens of retrieved specs per prompt
  max_specs: 5                      # specs picked by search (besides --spec)
  retries: 2                        # fix rounds after failed checks
  examples:                         # per kind, none by default: files whose conventions to follow
    ansible: [playbooks/site.yml]
  validate:                         # per kind, none by default: extra checks, run in the stage
    ansible: ["yamllint -s {files}"]
```

Environment variables override the `llm:` model settings after the file is read, for
unattended runs against a server docgen does not own (a repository with no `llm:` block
works too):

| Variable | Overrides |
|---|---|
| `DOCGEN_LLM_URL` | `llm.url`; unless `DOCGEN_LLM_START` is set too, also sets `start: none` and no `stop` |
| `DOCGEN_LLM_MODEL` | `llm.model` |
| `DOCGEN_LLM_MAX_SOURCE_TOKS` | `llm.max_source_toks` (a positive number) |
| `DOCGEN_LLM_MAX_RESPONSE_TOKS` | `llm.max_response_toks` (a positive number) |
| `DOCGEN_LLM_START` | `llm.start` (`none` disables) |
| `DOCGEN_LLM_STOP` | `llm.stop` (`none` disables) |
| `DOCGEN_LLM_EMBED_URL`, `DOCGEN_LLM_EMBED_MODEL` | `llm.embed_url`, `llm.embed_model` (a server named here is never started or stopped) |
| `DOCGEN_LLM_API_KEY` | none: sent as `Authorization: Bearer` on every request (a gateway's per-consumer key); environment only, never in `docgen.yaml` |

An empty variable counts as unset.

```bash
DOCGEN_LLM_URL=http://model-server:8080/v1 DOCGEN_LLM_MODEL=qwen3-coder-docgen docgen draft --all
```

## Develop

```bash
go test ./...
```

`internal/extract/testdata` holds one fixture per supported language.

## Contributing

Pull requests go into `dev`; [CONTRIBUTING.md](CONTRIBUTING.md) explains the branches, the
checks and how releases are cut.

## License

[MIT](LICENSE)
