# ask

<!-- docgen:begin id=internal/ask:overview scope=internal/ask hash=736c8ba68a12 -->
Package ask answers questions about a repository using local LLM model. It uses facts, documentation overviews, and source code to generate answers. The module checks answers for correctness and retries if needed.

The main types are Options to configure a question and Result to hold the answer and related data. The Answer function builds a prompt from repository data and asks the LLM. It checks the answer and retries if needed.

The buildPrompt function creates the input for the LLM. It includes the question, documentation overviews, exported symbols, and source code. It respects token limits by truncating source files. The system prompt guides the LLM to answer precisely from the provided material.
<!-- docgen:end id=internal/ask:overview -->

<!-- docgen:begin id=internal/ask:reference scope=internal/ask hash=736c8ba68a12 -->
## Reference

### `ask.go`

- **`Options`** (struct): Options bound one question.
- **`Result`** (struct): Result is an answer and what it was built from.
- **`Answer`** (function): Answer builds the prompt, asks once, checks, retries once with the problems listed.
<!-- docgen:end id=internal/ask:reference -->
