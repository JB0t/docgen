// Package llm is a minimal client for OpenAI-compatible chat servers such as
// llama.cpp's llama-server, LM Studio, vLLM or Ollama's /v1 endpoint.
package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

type Client struct {
	URL   string // base URL including /v1, e.g. http://127.0.0.1:8081/v1
	Model string
	HTTP  *http.Client
	// Progress, when set, is called as a completion streams in: once per batch of the
	// prompt that llama-server processes, once per answer chunk, and once at the end.
	Progress func(Progress)
}

// New is a client for the OpenAI-compatible server at url. DOCGEN_LLM_API_KEY, when set, is sent
// as a Bearer token on every request (an inference gateway's per-consumer key). It is read from
// the environment only, never from docgen.yaml, so a key is never committed with a repo's config.
func New(url, model string) *Client {
	h := &http.Client{Timeout: 30 * time.Minute}
	if key := os.Getenv("DOCGEN_LLM_API_KEY"); key != "" {
		h.Transport = bearer{key: key, base: http.DefaultTransport}
	}
	return &Client{URL: strings.TrimRight(url, "/"), Model: model, HTTP: h}
}

// bearer adds the API key to each request.
type bearer struct {
	key  string
	base http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return b.base.RoundTrip(r)
}

// CharsPerToken is the conservative ratio used to estimate token counts without a
// tokenizer: source code and prose with many identifiers tokenize at about 3-4
// characters per token for Qwen-style vocabularies, so 3.2 slightly overestimates.
const CharsPerToken = 3.2

// EstimateTokens estimates how many tokens text costs (always rounding up).
func EstimateTokens(text string) int {
	return int(math.Ceil(float64(utf8.RuneCountInString(text)) / CharsPerToken))
}

// ContextSize asks the server how many tokens one request may use. llama-server reports
// it on /props; servers without that endpoint (Ollama, vLLM) return 0 for "unknown".
func (c *Client) ContextSize() int {
	base := strings.TrimSuffix(c.URL, "/v1")
	response, err := c.HTTP.Get(base + "/props")
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0
	}
	var props struct {
		Settings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		TotalSlots int `json:"total_slots"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&props); err != nil {
		return 0
	}
	return props.Settings.NCtx
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Progress describes a streaming completion so far.
type Progress struct {
	PromptTotal, PromptDone int // prompt tokens to process, and processed (llama-server only)
	Tokens                  int // answer chunks received; about one token each
	Text                    string
	Elapsed                 time.Duration
	Done                    bool // the completion finished (or failed)
}

// Stats reports a completion's size and speed (from llama-server's timings when present).
type Stats struct {
	PromptTokens, CachedTokens, OutputTokens int
	Duration                                 time.Duration
}

func (s Stats) String() string {
	return fmt.Sprintf("%d prompt tokens (%d reused), %d output tokens, %s", s.PromptTokens, s.CachedTokens, s.OutputTokens, s.Duration.Round(time.Second))
}

// Healthy reports whether the server answers.
func (c *Client) Healthy() error {
	response, err := c.HTTP.Get(c.URL + "/models")
	if err != nil {
		return fmt.Errorf("no model server at %s (%w); start it with 'llm-server start', or point llm.url (or DOCGEN_LLM_URL) at a running one", c.URL, err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("model server at %s is not ready (HTTP %d)", c.URL, response.StatusCode)
	}
	return nil
}

// chunk is one server-sent event of a streamed completion.
type chunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"` // Ollama, OpenAI; llama-server reports timings.cache_n
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Timings *struct {
		CacheN *int `json:"cache_n"` // llama-server only; Ollama sends timings without it
	} `json:"timings"`
	PromptProgress *struct {
		Total     int `json:"total"`
		Processed int `json:"processed"`
	} `json:"prompt_progress"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat runs one deterministic completion, streamed so Progress can follow it.
func (c *Client) Chat(messages []Message, maxTokens int) (string, Stats, error) {
	body, _ := json.Marshal(map[string]any{
		"model": c.Model, "messages": messages, "temperature": 0, "max_tokens": maxTokens,
		"cache_prompt":    true, // llama-server: reuse the shared prefix of consecutive requests
		"stream":          true,
		"stream_options":  map[string]any{"include_usage": true},
		"return_progress": true, // llama-server: report prompt processing as it goes
	})
	started := time.Now()
	progress := Progress{}
	report := func() {
		if c.Progress != nil {
			progress.Elapsed = time.Since(started)
			c.Progress(progress)
		}
	}
	defer func() {
		progress.Done = true
		report()
	}()

	response, err := c.HTTP.Post(c.URL+"/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", Stats{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		var failure chunk
		if json.Unmarshal(data, &failure) == nil && failure.Error != nil {
			return "", Stats{}, fmt.Errorf("model server: %s", failure.Error.Message)
		}
		return "", Stats{}, fmt.Errorf("model server returned HTTP %d: %.200s", response.StatusCode, data)
	}

	var text strings.Builder
	var stats Stats
	finish := ""
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024) // one event can be large
	for scanner.Scan() {
		line := scanner.Text()
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok { // blank separators and ": keep-alive" comments
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			break
		}
		var event chunk
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return text.String(), stats, fmt.Errorf("unexpected event from %s: %.200s", c.URL, payload)
		}
		if event.Error != nil {
			return text.String(), stats, fmt.Errorf("model server: %s", event.Error.Message)
		}
		if event.PromptProgress != nil {
			progress.PromptTotal, progress.PromptDone = event.PromptProgress.Total, event.PromptProgress.Processed
			report()
		}
		if event.Usage != nil {
			stats.PromptTokens, stats.OutputTokens = event.Usage.PromptTokens, event.Usage.CompletionTokens
			if details := event.Usage.PromptTokensDetails; details != nil {
				stats.CachedTokens = details.CachedTokens
			}
		}
		if event.Timings != nil && event.Timings.CacheN != nil {
			stats.CachedTokens = *event.Timings.CacheN
		}
		for _, choice := range event.Choices {
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				progress.Tokens++
				progress.Text = text.String()
				report()
			}
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
			}
		}
	}
	stats.Duration = time.Since(started)
	if err := scanner.Err(); err != nil {
		return text.String(), stats, fmt.Errorf("reading the answer from %s: %w", c.URL, err)
	}
	if finish == "" {
		return text.String(), stats, fmt.Errorf("the model server ended the answer early (after %d chunks)", progress.Tokens)
	}
	if stats.OutputTokens == 0 {
		stats.OutputTokens = progress.Tokens
	}
	if finish == "length" {
		return text.String(), stats, fmt.Errorf("the answer was cut off at %d tokens", maxTokens)
	}
	return text.String(), stats, nil
}

// Embed returns one embedding per input from an OpenAI-compatible /embeddings endpoint
// (llama-server started with --embeddings, as llm-server's embed preset does).
func (c *Client) Embed(inputs []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": c.Model, "input": inputs})
	response, err := c.HTTP.Post(c.URL+"/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding server returned HTTP %d: %.300s", response.StatusCode, data)
	}
	var result struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unexpected reply from %s: %.200s", c.URL, data)
	}
	if len(result.Data) != len(inputs) {
		return nil, fmt.Errorf("got %d embeddings for %d inputs", len(result.Data), len(inputs))
	}
	vectors := make([][]float32, len(inputs))
	for position, item := range result.Data {
		index := item.Index
		if index < 0 || index >= len(inputs) {
			index = position
		}
		vectors[index] = item.Embedding
	}
	return vectors, nil
}
