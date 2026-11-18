# draft

<!-- docgen:begin id=internal/draft:overview scope=internal/draft hash=e77f0d43b703 -->
Package draft writes generated documentation sections into Markdown files. It handles drafting of module overview and usage sections. The module uses LLMs to generate text and checks the results for accuracy. It tracks which sections are up to date and avoids regenerating unchanged content.

The main entry points are `Module` and `CLIUsage`. `Module` drafts overview sections for Go packages. `CLIUsage` writes usage sections for command-line tools. Both functions use `Options` to control behaviour like skipping up-to-date sections or doing dry runs. They return `Result` structs describing what was done.

The package includes helpers to find modules under a root directory, check if sections are up to date, and extract existing section content. It also has utilities for building command tables and generating examples from LLM responses. The draft process reuses existing content when possible to avoid regenerating the same text.
<!-- docgen:end id=internal/draft:overview -->

<!-- docgen:begin id=internal/draft:reference scope=internal/draft hash=e77f0d43b703 -->
## Reference

### `draft.go`

- **`Options`** (struct): Options controls a draft run.
- **`Result`** (struct): Result reports what happened to one module.
- **`Module`** (function): Module drafts the sections for one module.
- **`Problems`** (function): Problems keeps the findings that mean a generated text claims something untrue (names, paths, flags or commands the code does not contain).
- **`ModuleInfo`** (struct): ModuleInfo is a directory worth documenting and the state of its generated sections.
- **`Modules`** (function): Modules finds directories under root (repo-relative, "" for all) that contain exported, non-test symbols, in path order. Each is one directory (not its subdirectories).
- **`Section`** (function): Section returns the body of a generated section (id "&lt;scope>:&lt;suffix>", e.g. "internal/notes:overview") from the module's README, or "" when there is none. It is how other commands reuse what draft already wrote instead of asking the model again.
- **`UpToDate`** (function): UpToDate reports, without the model, whether a module's sections match its sources.

### `usage.go`

- **`CLIUsage`** (function): CLIUsage writes a usage section for a configured CLI: an exact command table built from its scraped help, and model-written examples that are each checked against the command tree (failing examples are retried once, then dropped).
<!-- docgen:end id=internal/draft:reference -->
