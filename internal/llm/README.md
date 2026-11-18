# llm

<!-- docgen:begin id=internal/llm:overview scope=internal/llm hash=0708fe83226e -->
Package llm provides a minimal client for OpenAI-compatible chat servers. It supports models from llama.cpp's llama-server, LM Studio, vLLM, and Ollama. The client handles HTTP communication and streaming responses from the server.

The client struct holds the server URL, model name, and HTTP client. It can estimate token counts from text using a conservative ratio. The ContextSize method queries the server for maximum tokens allowed per request. The Healthy method checks if the server is reachable and ready.

The Chat method runs a deterministic completion with streaming support. It reports progress as the server sends chunks. The Progress callback receives updates on prompt processing and token generation. Stats from the completion include token counts and timing information. The client handles server-sent events and parses responses for errors and usage data.
<!-- docgen:end id=internal/llm:overview -->

<!-- docgen:begin id=internal/llm:reference scope=internal/llm hash=0708fe83226e -->
## Reference

### `llm.go`

- **`Client`** (struct)
- **`New`** (function)
- **`CharsPerToken`** (constant): CharsPerToken is the conservative ratio used to estimate token counts without a tokenizer: source code and prose with many identifiers tokenize at about 3-4 characters per token for Qwen-style vocabularies, so 3.2 slightly overestimates.
- **`EstimateTokens`** (function): EstimateTokens estimates how many tokens text costs (always rounding up).
- **`Client.ContextSize`** (method): ContextSize asks the server how many tokens one request may use. llama-server reports it on /props; servers without that endpoint (Ollama, vLLM) return 0 for "unknown".
- **`Message`** (struct)
- **`Progress`** (struct): Progress describes a streaming completion so far.
- **`Stats`** (struct): Stats reports a completion's size and speed (from llama-server's timings when present).
- **`Stats.String`** (method)
- **`Client.Healthy`** (method): Healthy reports whether the server answers.
- **`Client.Chat`** (method): Chat runs one deterministic completion, streamed so Progress can follow it.
<!-- docgen:end id=internal/llm:reference -->
