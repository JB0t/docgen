package config

import (
	"os"
	"path/filepath"
	"testing"
)

const localServer = `llm:
  url: http://127.0.0.1:8081/v1
  model: qwen3-coder-q8
  max_source_toks: 22000
  start: llm-server start
  stop: llm-server stop
`

func load(t *testing.T, yaml string, env map[string]string) *Config {
	t.Helper()
	for _, name := range []string{"DOCGEN_LLM_URL", "DOCGEN_LLM_MODEL", "DOCGEN_LLM_MAX_SOURCE_TOKS", "DOCGEN_LLM_MAX_RESPONSE_TOKS", "DOCGEN_LLM_START", "DOCGEN_LLM_STOP"} {
		t.Setenv(name, "") // t.Setenv restores the outer value afterwards
		os.Unsetenv(name)
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
	root := t.TempDir()
	if yaml != "" {
		if err := os.WriteFile(filepath.Join(root, FileName), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestFileWithoutEnv(t *testing.T) {
	cfg := load(t, localServer, nil)
	if cfg.LLM.URL != "http://127.0.0.1:8081/v1" || cfg.LLM.Model != "qwen3-coder-q8" || cfg.LLM.MaxSourceToks != 22000 ||
		cfg.LLM.MaxResponseToks != 6000 || cfg.LLM.Start != "llm-server start" || cfg.LLM.Stop != "llm-server stop" {
		t.Fatalf("LLM = %+v", cfg.LLM)
	}
}

func TestEnvURLOverridesFileAndDisablesStartStop(t *testing.T) {
	cfg := load(t, localServer, map[string]string{
		"DOCGEN_LLM_URL":               "http://192.0.2.10:11435/v1",
		"DOCGEN_LLM_MODEL":             "qwen3-coder-docgen",
		"DOCGEN_LLM_MAX_SOURCE_TOKS":   "18000",
		"DOCGEN_LLM_MAX_RESPONSE_TOKS": "7000",
	})
	if cfg.LLM.URL != "http://192.0.2.10:11435/v1" || cfg.LLM.Model != "qwen3-coder-docgen" ||
		cfg.LLM.MaxSourceToks != 18000 || cfg.LLM.MaxResponseToks != 7000 {
		t.Fatalf("LLM = %+v", cfg.LLM)
	}
	if cfg.LLM.Start != "none" || cfg.LLM.Stop != "" {
		t.Fatalf("a remote server must not be started or stopped: start %q stop %q", cfg.LLM.Start, cfg.LLM.Stop)
	}
}

func TestEnvWithoutLLMBlock(t *testing.T) {
	cfg := load(t, "exclude: [TODO.md]\n", map[string]string{"DOCGEN_LLM_URL": "http://remote/v1", "DOCGEN_LLM_MODEL": "m"})
	if cfg.LLM.URL != "http://remote/v1" || cfg.LLM.Model != "m" || cfg.LLM.Start != "none" || cfg.LLM.Stop != "" ||
		cfg.LLM.MaxSourceToks != 24000 || cfg.LLM.MaxResponseToks != 6000 {
		t.Fatalf("LLM = %+v", cfg.LLM)
	}
	if cfg := load(t, "", map[string]string{"DOCGEN_LLM_URL": "http://remote/v1"}); cfg.LLM.URL != "http://remote/v1" || cfg.LLM.Start != "none" {
		t.Fatalf("no docgen.yaml: LLM = %+v", cfg.LLM)
	}
}

func TestEnvStartStop(t *testing.T) {
	cfg := load(t, localServer, map[string]string{"DOCGEN_LLM_URL": "http://remote/v1", "DOCGEN_LLM_START": "wake-server", "DOCGEN_LLM_STOP": "none"})
	if cfg.LLM.Start != "wake-server" || cfg.LLM.Stop != "" {
		t.Fatalf("start %q stop %q", cfg.LLM.Start, cfg.LLM.Stop)
	}
	cfg = load(t, localServer, map[string]string{"DOCGEN_LLM_START": "none"})
	if cfg.LLM.URL != "http://127.0.0.1:8081/v1" || cfg.LLM.Start != "none" || cfg.LLM.Stop != "llm-server stop" {
		t.Fatalf("LLM = %+v", cfg.LLM)
	}
	cfg = load(t, localServer, map[string]string{"DOCGEN_LLM_URL": "http://remote/v1", "DOCGEN_LLM_START": ""})
	if cfg.LLM.Start != "none" || cfg.LLM.Stop != "" {
		t.Fatalf("an empty DOCGEN_LLM_START counts as unset: %+v", cfg.LLM)
	}
}

func TestEnvRejectsBadTokenCounts(t *testing.T) {
	for _, value := range []string{"lots", "0", "-5"} {
		t.Setenv("DOCGEN_LLM_MAX_SOURCE_TOKS", value)
		if _, err := Load(t.TempDir()); err == nil {
			t.Fatalf("DOCGEN_LLM_MAX_SOURCE_TOKS=%s: no error", value)
		}
	}
}

func TestEnvEmbedURLOverridesFileAndDisablesStartStop(t *testing.T) {
	cfg := load(t, localServer, map[string]string{
		"DOCGEN_LLM_EMBED_URL":   "http://inference-gateway.inference.svc.cluster.local:4000/v1",
		"DOCGEN_LLM_EMBED_MODEL": "embed",
	})
	if cfg.LLM.EmbedURL != "http://inference-gateway.inference.svc.cluster.local:4000/v1" || cfg.LLM.EmbedModel != "embed" {
		t.Fatalf("LLM = %+v", cfg.LLM)
	}
	if cfg.LLM.EmbedStart != "none" || cfg.LLM.EmbedStop != "" {
		t.Fatalf("a remote embedding server must not be started or stopped: start %q stop %q", cfg.LLM.EmbedStart, cfg.LLM.EmbedStop)
	}
}

func TestDraftSettings(t *testing.T) {
	cfg := load(t, "draft:\n  new_docs: [deploy, internal/collect/]\n  reference: true\n", nil)
	if !cfg.Draft.Reference {
		t.Fatal("draft.reference not read")
	}
	for scope, want := range map[string]bool{"deploy": true, "deploy/k8s": true, "internal/collect": true, "internal/collector": false, "internal": false, "cmd": false} {
		if got := cfg.Draft.AllowsNewDoc(scope); got != want {
			t.Errorf("AllowsNewDoc(%q) = %v, want %v", scope, got, want)
		}
	}
	if cfg := load(t, "", nil); cfg.Draft.Reference || cfg.Draft.AllowsNewDoc("cmd") {
		t.Fatalf("defaults: reference %v, new docs allowed in cmd", cfg.Draft.Reference)
	}
	if !(Draft{NewDocs: []string{"."}}).AllowsNewDoc("any/module") {
		t.Fatal(`new_docs: ["."] should allow every module`)
	}
}
