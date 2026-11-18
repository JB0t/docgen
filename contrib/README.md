# contrib

Companion scripts for docgen. Each prints its usage with `--help`.

| Script | What it does |
| --- | --- |
| `llm-fit` | Picks the largest local models and contexts that fit this machine (CPU, RAM, GPU, disk) for `llm-server`, and writes the choice to `~/.config/llm-server/env`. |
| `llm-server` | Runs llama.cpp's `llama-server` with presets (`code` for docgen, `digest` and `embed` for other tools): `start`, `stop`, `status`, `log`, `download`. |
| `diffgen` | Finds the files a change concerns with `git grep` and runs `docgen code` on them from the repository root. |
| `docgen-cron` | Runs `docgen draft --all` and `docgen check` in a list of repositories on a schedule. |
| `docgen-job` | The same for a Kubernetes CronJob: builds docgen, clones each repository, and opens a GitLab MR or GitHub PR with what changed. `tests/docgen-job-test` tests it with local repositories and fake APIs. |

Put this directory on your `PATH`, or copy the scripts you use somewhere that is.
