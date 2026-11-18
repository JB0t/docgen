package llm

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestChatStreamsWithProgress(t *testing.T) {
	events := []string{
		`{"choices":[],"prompt_progress":{"total":100,"cache":0,"processed":50,"time_ms":5}}`,
		`{"choices":[],"prompt_progress":{"total":100,"cache":0,"processed":100,"time_ms":9}}`,
		`{"choices":[{"delta":{"role":"assistant","content":null},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":"diff --git a/x b/x\n"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":" \n"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":2},"timings":{"cache_n":40}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": keep-alive\n\n"))
		for _, event := range events {
			_, _ = w.Write([]byte("data: " + event + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := New(server.URL+"/v1", "m")
	var seen []Progress
	client.Progress = func(progress Progress) { seen = append(seen, progress) }
	text, stats, err := client.Chat([]Message{{Role: "user", Content: "hi"}}, 50)
	if err != nil || text != "diff --git a/x b/x\n \n" {
		t.Fatalf("Chat = %q, %v", text, err)
	}
	if stats.PromptTokens != 100 || stats.CachedTokens != 40 || stats.OutputTokens != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	if len(seen) != 5 || seen[1].PromptDone != 100 || seen[3].Tokens != 2 || !seen[4].Done {
		t.Fatalf("progress = %+v", seen)
	}
}

func TestChatReportsEarlyEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"diff"},"finish_reason":null}]}` + "\n\n"))
	}))
	defer server.Close()
	text, _, err := New(server.URL+"/v1", "m").Chat(nil, 50)
	if err == nil || text != "diff" || !strings.Contains(err.Error(), "early") {
		t.Fatalf("Chat = %q, %v", text, err)
	}
}

// TestLiveServer runs against a real server when DOCGEN_LIVE_URL is set; Ollama also
// needs DOCGEN_LIVE_MODEL (llama-server ignores the model name).
func TestLiveServer(t *testing.T) {
	url := os.Getenv("DOCGEN_LIVE_URL")
	if url == "" {
		t.Skip("DOCGEN_LIVE_URL not set")
	}
	client := New(url, os.Getenv("DOCGEN_LIVE_MODEL"))
	if err := client.Healthy(); err != nil {
		t.Fatal(err)
	}
	var last Progress
	prompts := 0
	client.Progress = func(progress Progress) {
		if progress.PromptTotal > 0 && progress.Tokens == 0 {
			prompts++
		}
		last = progress
	}
	text, stats, err := client.Chat([]Message{{Role: "user", Content: strings.Repeat("pear ", 700) + "/no_think Count to three."}}, 200)
	t.Logf("answer %q, stats %s, prompt progress events %d, last %+v", text, stats, prompts, last.Done)
	if err != nil || text == "" || !last.Done {
		t.Fatal(err)
	}
}

func TestEstimateTokensRoundsUp(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("empty = %d", got)
	}
	if got := EstimateTokens("abc"); got != 1 {
		t.Fatalf("3 chars = %d, want 1", got)
	}
	if got := EstimateTokens(strings.Repeat("x", 320)); got != 100 {
		t.Fatalf("320 chars = %d, want 100", got)
	}
}

func TestContextSizeReadsLlamaServerProps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/props" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":32768},"total_slots":1}`))
	}))
	defer server.Close()
	if got := New(server.URL+"/v1", "m").ContextSize(); got != 32768 {
		t.Fatalf("ContextSize = %d", got)
	}
	if got := New(server.URL+"/nowhere/v1", "m").ContextSize(); got != 0 {
		t.Fatalf("unknown server = %d, want 0", got)
	}
}

// ollamaStream is a stream recorded from Ollama 0.40.2's /v1/chat/completions (stream_options
// include_usage): every chunk repeats id/model/system_fingerprint, the finish comes with an
// empty delta, usage arrives in a last chunk with no choices (cached tokens under
// prompt_tokens_details, Ollama's own timings without cache_n), then [DONE]. The unknown
// request fields docgen sends for llama-server (cache_prompt, return_progress) are ignored.
// Only cached_tokens is edited (0 in the recording) to show it is read.
const ollamaStream = `data: {"id":"chatcmpl-667","object":"chat.completion.chunk","created":1793284568,"model":"qwen3:4b-instruct-2507-q4_K_M","system_fingerprint":"fp_ollama","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-667","object":"chat.completion.chunk","created":1793284568,"model":"qwen3:4b-instruct-2507-q4_K_M","system_fingerprint":"fp_ollama","choices":[{"index":0,"delta":{"content":" there"},"finish_reason":null}]}

data: {"id":"chatcmpl-667","object":"chat.completion.chunk","created":1793284568,"model":"qwen3:4b-instruct-2507-q4_K_M","system_fingerprint":"fp_ollama","choices":[{"index":0,"delta":{"content":" friend"},"finish_reason":null}]}

data: {"id":"chatcmpl-667","object":"chat.completion.chunk","created":1793284568,"model":"qwen3:4b-instruct-2507-q4_K_M","system_fingerprint":"fp_ollama","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: {"id":"chatcmpl-667","object":"chat.completion.chunk","created":1793284568,"model":"qwen3:4b-instruct-2507-q4_K_M","system_fingerprint":"fp_ollama","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-667","object":"chat.completion.chunk","created":1793284568,"model":"qwen3:4b-instruct-2507-q4_K_M","system_fingerprint":"fp_ollama","choices":[],"usage":{"prompt_tokens":14,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens":5,"total_tokens":19},"timings":{"prompt_n":14,"prompt_ms":243,"prompt_per_token_ms":17.357142857142858,"prompt_per_second":57.61316872427984,"predicted_n":5,"predicted_ms":213,"predicted_per_token_ms":42.6,"predicted_per_second":23.474178403755868}}

data: [DONE]

`

func TestChatOllamaStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models": // Ollama lists its models in OpenAI's shape
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen3:4b-instruct-2507-q4_K_M","object":"model","created":1791675943,"owned_by":"library"}]}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(strings.ReplaceAll(ollamaStream, "\n", "\r\n"))) // CRLF must not matter
		default:
			http.NotFound(w, r) // no /props: the context size is unknown
		}
	}))
	defer server.Close()
	client := New(server.URL+"/v1/", "qwen3:4b-instruct-2507-q4_K_M")
	if err := client.Healthy(); err != nil {
		t.Fatal(err)
	}
	if got := client.ContextSize(); got != 0 {
		t.Fatalf("ContextSize = %d, want 0 (unknown)", got)
	}
	var last Progress
	client.Progress = func(progress Progress) { last = progress }
	text, stats, err := client.Chat([]Message{{Role: "user", Content: "Say hi in three words."}}, 20)
	if err != nil || text != "Hello there friend!" {
		t.Fatalf("Chat = %q, %v", text, err)
	}
	if stats.PromptTokens != 14 || stats.OutputTokens != 5 || stats.CachedTokens != 3 {
		t.Fatalf("stats = %+v", stats)
	}
	if !last.Done || last.Tokens != 4 {
		t.Fatalf("last progress = %+v", last)
	}
}

func TestChatWithoutUsageOrDone(t *testing.T) {
	// Servers that ignore stream_options send no usage chunk, and some close without [DONE].
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata:{\"choices\":[{\"delta\":{\"content\":\"b\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer server.Close()
	text, stats, err := New(server.URL+"/v1", "m").Chat(nil, 50)
	if err != nil || text != "ab" || stats.OutputTokens != 2 {
		t.Fatalf("Chat = %q, %+v, %v", text, stats, err)
	}
}
