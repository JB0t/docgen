# docgen

<!-- docgen:begin id=cli:docgen:usage scope=. hash=702a2430a7be -->
## Usage

| Command | Description | Flags |
|---|---|---|
| `docgen check` | Check docs against the code: commands, flags, paths, symbols, markers | `--apply` `--coverage` `--fix` `--json` `--strict` |
| `docgen ask` | Answer a question about the code with the local model, checked against the facts |  |
| `docgen code` | Generate a scoped code patch with the local model | `--apply` `--format` `--request` |
| `docgen completion` | Generate the autocompletion script for the specified shell |  |
| `docgen draft` | Write module overviews (local model), exact references and CLI usage into READMEs | `--all` `--cli` `--doc` `--dry-run` `--force` |
| `docgen facts` | Extract facts (files, symbols, env vars, docs, CLI commands) as JSON | `-o` `--output` `--summary` |
| `docgen modules` | List the modules 'draft --all' would document, and their state |  |

Global flags: `-v` `--version`

### Examples

### `docgen code`

Generate a code patch for review; `--apply` applies it after Git checks it.
```bash
# Preview a focused change
docgen code --request "Move timer commands from tasks to clctl timers list" \
  cmd/root.go cmd/tasks.go cmd/timers.go

# Apply only after checking the patch
docgen code --apply --request "Rename the command" cmd/timers.go
```

### `docgen check`

Validates documentation against codebase to ensure all references are accurate and complete.
```bash
# Check documentation in current directory
docgen check .

# Check documentation in specific repository with JSON output
docgen check /path/to/repo --json

# Check documentation in strict mode to fail on warnings
docgen check /path/to/repo --strict
```

### `docgen draft`

Use to automatically generate documentation drafts for modules or CLIs based on code facts.
```bash
# Draft documentation for a specific module
docgen draft internal/notes

# Draft with dry-run to preview changes
docgen draft --dry-run internal

# Draft CLI usage section from docgen.yaml
docgen draft --cli mytool --doc USAGE.md
```

### `docgen facts`

Use to scan repository and extract factual information about code structure and documentation.
```bash
# Generate facts for current directory
docgen facts .

# Generate facts with summary output
docgen facts --summary

# Generate facts to a specific file
docgen facts -o facts.json
```

### `docgen modules`

Use to list all modules that would be processed by 'docgen draft --all' and their current state.
```bash
# List modules in current directory
docgen modules .

# List modules in specific root directory
docgen modules /path/to/repo
```
<!-- docgen:end id=cli:docgen:usage -->
