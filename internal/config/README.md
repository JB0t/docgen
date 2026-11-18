# config

<!-- docgen:begin id=internal/config:overview scope=internal/config hash=4b3456e5f062 -->
Package config reads the `docgen.yaml` file from a repository root. It defines how docgen scans files and Markdown docs. The config file is optional. Without it docgen scans all tracked files and Markdown docs.

The config struct holds settings for excluding paths, matching documentation files, defining CLI tools, and LLM server settings. Each CLI defines how to run a command-line tool and where to write usage sections. The LLM struct configures an OpenAI-compatible model server.

The Load function reads and parses `docgen.yaml`. It sets default values for missing fields. The Excluded method checks if a path matches any exclude pattern. The IsDoc method checks if a path matches documentation patterns. The match function supports `**` for directory matching.
<!-- docgen:end id=internal/config:overview -->

<!-- docgen:begin id=internal/config:reference scope=internal/config hash=4b3456e5f062 -->
## Reference

### `config.go`

- **`FileName`** (constant)
- **`CLI`** (struct): CLI is a command-line tool whose --help output becomes facts (commands and flags).
- **`Config`** (struct)
- **`LLM`** (struct): LLM settings; defaults match the llm-server launcher.
- **`Load`** (function): Load reads &lt;root>/docgen.yaml if present.
- **`Config.Excluded`** (method): Excluded reports whether a repo-relative path matches an exclude pattern, either as a whole or in any of its directory segments.
- **`Config.IsDoc`** (method): IsDoc reports whether a path matches the docs patterns.
<!-- docgen:end id=internal/config:reference -->
