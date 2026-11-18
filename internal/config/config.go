// Package config reads docgen.yaml from the repository root. Every setting is
// optional; without the file docgen scans all tracked files and Markdown docs.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const FileName = "docgen.yaml"

// CLI is a command-line tool whose --help output becomes facts (commands and flags).
type CLI struct {
	Name string   `yaml:"name"` // how the docs invoke it, e.g. clctl
	Run  []string `yaml:"run"`  // how to run it from the repo root, e.g. [go, run, ./cmd/mytool]
	Dir  string   `yaml:"dir"`  // working directory relative to the repo root
	Doc  string   `yaml:"doc"`  // where draft writes its usage section (default <dir>/README.md)
	// Env is set while scraping --help, for tools whose commands depend on settings.
	Env map[string]string `yaml:"env"`
}

type Config struct {
	// Exclude holds glob patterns (matched against repo-relative paths and each path
	// segment) for sources and docs to skip.
	Exclude []string `yaml:"exclude"`
	// userExclude is Exclude as the repository wrote it, before the defaults were added.
	userExclude []string
	// Docs holds glob patterns for documentation files; default **/*.md.
	Docs []string `yaml:"docs"`
	// CLIs to scrape for commands and flags.
	CLIs []CLI `yaml:"clis"`
	// Ignore holds references the checker should never report (e.g. external tools).
	Ignore []string `yaml:"ignore"`
	// LLM is the OpenAI-compatible model server used by draft.
	LLM LLM `yaml:"llm"`
	// Draft holds settings for docgen draft.
	Draft Draft `yaml:"draft"`
	// Gen holds settings for docgen gen (spec retrieval, examples, extra checks).
	Gen Gen `yaml:"gen"`
	// Rescan ignores cached CLI command trees (set by --rescan, not from the file).
	Rescan bool `yaml:"-"`
}

// LLM settings; defaults match the llm-server launcher. The DOCGEN_LLM_* environment
// variables override the file (see applyEnv), for unattended runs against a remote server.
type LLM struct {
	URL             string `yaml:"url"`               // default http://127.0.0.1:8081/v1
	Model           string `yaml:"model"`             // default qwen3-coder
	MaxSourceToks   int    `yaml:"max_source_toks"`   // source code per module prompt; default 24000
	MaxResponseToks int    `yaml:"max_response_toks"` // response size per prompt; default 6000
	// Start and Stop are shell commands draft runs when the server is not already up,
	// so the model only holds its memory while drafting. Defaults: llm-server start/stop.
	// Set start to "none" to manage the server yourself.
	Start string `yaml:"start"`
	Stop  string `yaml:"stop"`
	// The embedding server used to search specs (docgen spec, docgen gen). Defaults match
	// llm-server's embed preset; set embed_start to "none" to manage it yourself, and
	// embed_url to "none" to search specs by keywords only.
	EmbedURL   string `yaml:"embed_url"`   // default http://127.0.0.1:8083/v1
	EmbedModel string `yaml:"embed_model"` // default nomic-embed-text
	EmbedStart string `yaml:"embed_start"` // default llm-server start embed
	EmbedStop  string `yaml:"embed_stop"`  // default llm-server stop embed
}

// Draft settings for docgen draft.
type Draft struct {
	// NewDocs lists directories (repo-relative) where 'draft --all' may start generated
	// sections in a module that has none yet; a module in or under one of them counts.
	// Everywhere else --all only redrafts sections that already exist, so a nightly run
	// does not add a README to every package. Naming a module directory on the command
	// line, or --new-docs, drafts it anyway.
	NewDocs []string `yaml:"new_docs"`
	// Reference adds the exact list of exported symbols to each module's README. Off by
	// default: godoc and the source already show it, and any change to the module makes
	// it stale. When off, an existing reference section goes when its module is redrafted.
	Reference bool `yaml:"reference"`
}

// AllowsNewDoc reports whether 'draft --all' may start generated sections for a module.
func (d Draft) AllowsNewDoc(scope string) bool {
	scope = strings.Trim(filepath.ToSlash(scope), "/")
	for _, dir := range d.NewDocs {
		dir = strings.Trim(filepath.ToSlash(dir), "/")
		if dir == "" || dir == "." || scope == dir || strings.HasPrefix(scope, dir+"/") {
			return true
		}
	}
	return false
}

// Gen settings for docgen gen.
type Gen struct {
	// SpecToks is how many tokens of retrieved specs a prompt may hold; default 8000.
	SpecToks int `yaml:"spec_toks"`
	// MaxSpecs caps how many specs are picked automatically; default 5.
	MaxSpecs int `yaml:"max_specs"`
	// Retries is how many times failed checks are sent back to the model; default 2.
	Retries int `yaml:"retries"`
	// Examples lists, per kind, files whose conventions generated files should follow.
	Examples map[string][]string `yaml:"examples"`
	// Validate lists, per kind, extra commands to run on generated files. {files} is
	// replaced by the changed files and {dir} by their common directory; a command that
	// exits non-zero fails the check and its output goes back to the model.
	Validate map[string][]string `yaml:"validate"`
}

var defaultExclude = []string{".git", "node_modules", "vendor", "venvs", ".venv", "venv", "__pycache__", "dist", "build", "target", "bin", "obj", ".terraform", "*.pyc", "*.lock", "go.sum", "testdata",
	// Terraform state and plans can hold secrets; the lock file is provider hashes.
	"*.tfstate", "*.tfstate.*", "*.tfplan", ".terraform.lock.hcl"}

// Load reads <root>/docgen.yaml if present.
func Load(root string) (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := applyEnv(&cfg.LLM); err != nil {
		return nil, err
	}
	cfg.userExclude = append([]string(nil), cfg.Exclude...)
	cfg.Exclude = append(cfg.Exclude, defaultExclude...)
	if len(cfg.Docs) == 0 {
		cfg.Docs = []string{"**/*.md"}
	}
	if cfg.LLM.URL == "" {
		cfg.LLM.URL = "http://127.0.0.1:8081/v1"
	}
	if cfg.LLM.Model == "" {
		cfg.LLM.Model = "qwen3-coder"
	}
	if cfg.LLM.MaxSourceToks == 0 {
		cfg.LLM.MaxSourceToks = 24000
	}
	if cfg.LLM.Start == "" {
		cfg.LLM.Start, cfg.LLM.Stop = "llm-server start", "llm-server stop"
	}
	if cfg.LLM.MaxResponseToks == 0 {
		cfg.LLM.MaxResponseToks = 6000
	}
	if cfg.LLM.Stop == "none" {
		cfg.LLM.Stop = ""
	}
	if cfg.LLM.EmbedURL == "" {
		cfg.LLM.EmbedURL = "http://127.0.0.1:8083/v1"
	}
	if cfg.LLM.EmbedModel == "" {
		cfg.LLM.EmbedModel = "nomic-embed-text"
	}
	if cfg.LLM.EmbedStart == "" {
		cfg.LLM.EmbedStart, cfg.LLM.EmbedStop = "llm-server start embed", "llm-server stop embed"
	}
	if cfg.Gen.SpecToks == 0 {
		cfg.Gen.SpecToks = 8000
	}
	if cfg.Gen.MaxSpecs == 0 {
		cfg.Gen.MaxSpecs = 5
	}
	if cfg.Gen.Retries == 0 {
		cfg.Gen.Retries = 2
	}
	return cfg, nil
}

// applyEnv overrides the model settings from the environment, after docgen.yaml is read:
// DOCGEN_LLM_URL, DOCGEN_LLM_MODEL, DOCGEN_LLM_MAX_SOURCE_TOKS, DOCGEN_LLM_MAX_RESPONSE_TOKS,
// DOCGEN_LLM_START and DOCGEN_LLM_STOP ("none" disables either), DOCGEN_LLM_EMBED_URL and
// DOCGEN_LLM_EMBED_MODEL (the embedding server, never started or stopped); an empty variable counts
// as unset. A server named by
// DOCGEN_LLM_URL is somebody else's to run, so unless DOCGEN_LLM_START is set too,
// docgen neither starts nor stops it, whatever docgen.yaml says.
func applyEnv(llm *LLM) error {
	if url := os.Getenv("DOCGEN_LLM_URL"); url != "" {
		llm.URL = url
		if os.Getenv("DOCGEN_LLM_START") == "" {
			llm.Start, llm.Stop = "none", ""
		}
	}
	if model := os.Getenv("DOCGEN_LLM_MODEL"); model != "" {
		llm.Model = model
	}
	// The embedding server likewise (spec search): DOCGEN_LLM_EMBED_URL is somebody else's to run.
	if url := os.Getenv("DOCGEN_LLM_EMBED_URL"); url != "" {
		llm.EmbedURL = url
		llm.EmbedStart, llm.EmbedStop = "none", ""
	}
	if model := os.Getenv("DOCGEN_LLM_EMBED_MODEL"); model != "" {
		llm.EmbedModel = model
	}
	for name, target := range map[string]*int{
		"DOCGEN_LLM_MAX_SOURCE_TOKS":   &llm.MaxSourceToks,
		"DOCGEN_LLM_MAX_RESPONSE_TOKS": &llm.MaxResponseToks,
	} {
		value := os.Getenv(name)
		if value == "" {
			continue
		}
		number, err := strconv.Atoi(value)
		if err != nil || number <= 0 {
			return fmt.Errorf("%s=%q: want a positive number of tokens", name, value)
		}
		*target = number
	}
	if start := os.Getenv("DOCGEN_LLM_START"); start != "" {
		llm.Start = start
	}
	if stop := os.Getenv("DOCGEN_LLM_STOP"); stop != "" {
		llm.Stop = stop
	}
	return nil
}

// Excluded reports whether a repo-relative path matches an exclude pattern, either as a
// whole or in any of its directory segments.
func (c *Config) Excluded(path string) bool { return excluded(c.Exclude, path) }

// ExcludedByRepo is Excluded with only the patterns the repository's docgen.yaml lists,
// not the built-in defaults (bin, build, dist, vendor, ...). A file git tracks inside such a
// directory is source, not build output: a scripts repository keeps its commands in bin/.
func (c *Config) ExcludedByRepo(path string) bool { return excluded(c.userExclude, path) }

func excluded(patterns []string, path string) bool {
	slashed := filepath.ToSlash(path)
	for _, pattern := range patterns {
		if match(pattern, slashed) {
			return true
		}
		for _, segment := range splitPath(slashed) {
			if ok, _ := filepath.Match(pattern, segment); ok {
				return true
			}
		}
	}
	return false
}

// IsDoc reports whether a path matches the docs patterns.
func (c *Config) IsDoc(path string) bool {
	for _, pattern := range c.Docs {
		if match(pattern, filepath.ToSlash(path)) {
			return true
		}
	}
	return false
}

func splitPath(path string) []string {
	var segments []string
	current := ""
	for _, char := range path {
		if char == '/' {
			segments = append(segments, current)
			current = ""
			continue
		}
		current += string(char)
	}
	return append(segments, current)
}

// match supports ** for any number of directories.
func match(pattern, path string) bool {
	if ok, _ := filepath.Match(pattern, path); ok {
		return true
	}
	const any = "**/"
	for len(pattern) >= len(any) && pattern[:len(any)] == any {
		rest := pattern[len(any):]
		for index := 0; index <= len(path); index++ {
			if index == 0 || path[index-1] == '/' {
				if ok, _ := filepath.Match(rest, path[index:]); ok {
					return true
				}
			}
		}
		return false
	}
	return false
}
