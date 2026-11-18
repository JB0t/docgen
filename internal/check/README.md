# check

<!-- docgen:begin id=internal/check:overview scope=internal/check hash=e9bc3cecf17d -->
Package check validates documentation against the facts. It ensures every command, flag, path, symbol and environment variable a doc mentions actually exists. Generated sections must have intact markers and must not be stale. The package also reports on coverage: commands, flags and env vars that docs do not mention.

The checker processes each document and scans for inline code spans, shell code blocks, and generated section markers. It identifies CLI commands and their flags within shell blocks. It validates that paths exist relative to the document or repository root. It checks that symbols, environment variables, and flags are defined or used in the codebase.

The main entry points are `Run` and `Text`. `Run` checks all docs in the facts. `Text` checks Markdown text that has not been written to disk yet. Both functions return a slice of findings. Findings include errors, warnings, and info messages about documentation problems. The checker tracks what docs reference and reports on coverage when requested.
<!-- docgen:end id=internal/check:overview -->

<!-- docgen:begin id=internal/check:reference scope=internal/check hash=e9bc3cecf17d -->
## Reference

### `check.go`

- **`Severity`** (type)
- **`Error`** (constant)
- **`Warning`** (constant)
- **`Info`** (constant)
- **`Finding`** (struct): Finding is one problem in one doc.
- **`Finding.String`** (method)
- **`Options`** (struct): Options control what is checked.
- **`Run`** (function): Run checks every doc in the facts.
- **`Text`** (function): Text checks Markdown that is not a file yet (e.g. a generated draft). path is where it will be written, which relative paths are resolved against.
<!-- docgen:end id=internal/check:reference -->
