# facts

<!-- docgen:begin id=internal/facts:overview scope=internal/facts hash=4f02be6e0a7d -->
Package facts builds a repository's fact sheet from its source code. It scans files, extracts symbols and environment variables, and records CLI command trees from `--help` output. The facts are derived without AI so they serve as ground truth for documentation.

The module has two main parts. The `facts` package builds the full facts sheet including files, languages, symbols, environment variables, documentation, and CLI commands. The `cli` package scrapes CLI tools recursively to build command trees from their help output.

CLI scraping uses caching to avoid rebuilding the same command tree. The `CLI` struct holds the parsed command tree and provides methods to query flags, command paths, and value types. The `Facts` struct holds all repository facts and provides methods to check for symbols, environment variables, and file hashes.
<!-- docgen:end id=internal/facts:overview -->

<!-- docgen:begin id=internal/facts:reference scope=internal/facts hash=4f02be6e0a7d -->
## Reference

### `cli.go`

- **`CLI`** (struct): CLI is a command-line tool's command tree, scraped from its --help output.
- **`Command`** (struct): Command is one (sub)command: its path below the tool ("" for the root, "tasks list").
- **`ScrapeCLI`** (function): ScrapeCLI runs the tool's --help recursively (three levels deep) and records each command's flags. `go run` tools are built once first so scraping stays fast.
- **`CLI.ValueType`** (method): ValueType returns a flag's value type for the command or a parent, or "".
- **`CLI.TakesValue`** (method): TakesValue reports whether a flag of the command (or a parent) takes a value.
- **`CLI.WrappedBy`** (method): WrappedBy returns the wrapper command (e.g. "kubectl") that path sits under, or "".
- **`CLI.Child`** (method): Child returns the path of the subcommand of parent called word, by name or alias.
- **`CLI.Find`** (method): Find returns the command at path ("tasks list") or false.
- **`CLI.HasFlag`** (method): HasFlag reports whether a command (or one of its parents, for inherited flags) accepts the flag.

### `facts.go`

- **`Version`** (constant)
- **`File`** (struct)
- **`Doc`** (struct)
- **`Facts`** (struct)
- **`Build`** (function): Build scans the repository at root.
- **`Facts.Mentioned`** (method): Mentioned reports whether a word appears anywhere in the source code.
- **`Facts.HasSymbol`** (method): HasSymbol reports whether a symbol with this name exists (also matching Parent.Name).
- **`Facts.HasEnvVar`** (method): HasEnvVar reports whether the code reads this environment variable.
- **`Facts.FileHash`** (method): FileHash returns a combined hash of every non-documentation file under scope (a file or directory). Docs are left out so that writing a module's README does not make the sections in it stale.
- **`Facts.Write`** (method): Write saves the facts as JSON.
<!-- docgen:end id=internal/facts:reference -->
