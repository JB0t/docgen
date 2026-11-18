# cmd

<!-- docgen:begin id=cmd:overview scope=cmd hash=ec63f1f6929e -->
Package cmd implements the command-line interface for the docgen tool. It defines subcommands for asking questions about code, generating code patches, and fixing documentation issues using a local language model. The package uses Cobra for command parsing and integrates with the LLM server through a shared model session.

The module manages a model session that starts and stops the LLM server process as needed. It ensures the server is ready before making requests and handles shutdown gracefully on interrupt signals. The session also checks prompt and response token budgets to prevent oversized requests that would exceed the server's context window.

Commands share common functionality through the modelSession type. The ask command queries the model about code facts and validates answers against the actual code. The code command generates patches for code changes and can apply them with git. The fix command corrects documentation issues by having the model fix specific findings. Each command loads configuration, determines file scopes, builds prompts, and handles model responses with retry logic for failed patches or answers.
<!-- docgen:end id=cmd:overview -->

<!-- docgen:begin id=cmd:reference scope=cmd hash=ec63f1f6929e -->
## Reference

### `model.go`

- **`modelSession.Ensure`** (method): Ensure makes sure the server answers, starting it with llm.start when configured.
- **`modelSession.Stop`** (method): Stop stops the server if this run started it. Safe to call more than once.
- **`modelSession.CheckBudget`** (method): CheckBudget fails before the model is asked when the prompt plus the answer cannot fit the server's context (known for llama-server; unknown servers are not checked), so an oversized request is an error here, never a silently cut prompt.

### `root.go`

- **`Execute`** (function): Execute runs docgen and returns the exit code.
- **`exitCode.Error`** (method)
<!-- docgen:end id=cmd:reference -->
