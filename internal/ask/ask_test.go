package ask

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/facts"
	"github.com/JB0t/docgen/internal/llm"
)

func TestAnswerUsesOverviewsAndRetriesOnBadReferences(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("cmd/timers.go", "package cmd\n\n// ListTimers prints running timers.\nfunc ListTimers() {}\n")
	write("cmd/README.md", "# cmd\n\n<!-- docgen:begin id=cmd:overview scope=cmd hash=abc -->\nThe cmd package holds the CLI commands; timers live in timers.go.\n<!-- docgen:end id=cmd:overview -->\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, output)
		}
	}
	cfg := &config.Config{Docs: []string{"**/*.md"}} // what config.Load sets by default
	f, err := facts.Build(root, cfg)
	if err != nil {
		t.Fatal(err)
	}

	var prompts []string
	answers := []string{"Use `ListTimersFast` in `cmd/timers.go`.", "Use `ListTimers` in `cmd/timers.go`."}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		prompts = append(prompts, body.Messages[len(body.Messages)-1].Content)
		answer := answers[0]
		if len(answers) > 1 {
			answers = answers[1:]
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": answer}, "finish_reason": "stop"}}})
		_, _ = w.Write([]byte("data: " + string(event) + "\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	result, err := Answer(f, cfg, llm.New(server.URL+"/v1", "m"), Options{Question: "how do I list timers?", MaxToks: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 {
		t.Fatalf("expected one retry, got %d calls", len(prompts))
	}
	if !strings.Contains(prompts[0], "timers live in timers.go") || !strings.Contains(prompts[0], `<file path="cmd/timers.go">`) {
		t.Fatalf("first prompt lacks the overview or the source:\n%s", prompts[0])
	}
	if !strings.Contains(prompts[1], "ListTimersFast") {
		t.Fatalf("retry prompt does not list the bad reference:\n%s", prompts[1])
	}
	if result.Answer != answers[0] || len(result.Findings) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Files) != 1 || result.Files[0] != "cmd/timers.go" {
		t.Fatalf("files = %v", result.Files)
	}
}
