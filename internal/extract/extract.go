// Package extract pulls documentation facts out of source files without any AI:
// declared symbols with their signatures and doc comments, and environment variables
// read by the code. Each language has its own extractor; structured parsers are used
// where they are cheap (Go, Terraform, YAML, Python via its own ast module) and
// line-based patterns elsewhere.
package extract

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Symbol is one declared thing worth documenting.
type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // function, method, type, class, struct, enum, trait, resource, variable, output, ...
	Language  string `json:"language"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Parent    string `json:"parent,omitempty"`
	Signature string `json:"signature,omitempty"`
	Doc       string `json:"doc,omitempty"`
	Exported  bool   `json:"exported"`
}

// EnvVar is an environment variable read by the code.
type EnvVar struct {
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// Result is what an extractor found in one file.
type Result struct {
	Symbols []Symbol
	EnvVars []EnvVar
}

// Extractor handles one language.
type Extractor interface {
	Language() string
	Extract(path string, source []byte) Result
}

var shebangRE = regexp.MustCompile(`^#!\s*\S*?(?:/env\s+)?(bash|sh|zsh|python3?|Rscript)\b`)

// Language detects a file's language from its path and first line; "" if unsupported.
func Language(path string, head []byte) string {
	base := strings.ToLower(filepath.Base(path))
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".tf", ".tfvars", ".hcl": // .hcl: Terragrunt, Terraform tests (.tftest.hcl)
		return "terraform"
	case ".sh", ".bash":
		return "bash"
	case ".cs":
		return "csharp"
	case ".rs":
		return "rust"
	case ".r":
		return "r"
	case ".yml", ".yaml":
		return yamlFlavour(path)
	}
	if base == "chart.yaml" {
		return "helm"
	}
	if match := shebangRE.FindSubmatch(head); match != nil {
		switch string(match[1]) {
		case "bash", "sh", "zsh":
			return "bash"
		case "python", "python3":
			return "python"
		case "Rscript":
			return "r"
		}
	}
	return ""
}

// yamlFlavour tells Helm charts and Ansible content apart from other YAML by location.
func yamlFlavour(path string) string {
	slashed := filepath.ToSlash(path)
	base := strings.ToLower(filepath.Base(path))
	switch {
	case base == "chart.yaml" || base == "values.yaml" || strings.Contains(slashed, "/templates/") && hasChartAbove(slashed):
		return "helm"
	case strings.Contains(slashed, "roles/") || strings.Contains(slashed, "playbooks/") || strings.HasPrefix(base, "playbook") || strings.Contains(slashed, "group_vars/") || strings.Contains(slashed, "host_vars/"):
		return "ansible"
	}
	return "yaml"
}

func hasChartAbove(path string) bool {
	return strings.Contains(path, "/charts/") || strings.Contains(path, "chart")
}

// All returns every extractor, keyed by language.
func All() map[string]Extractor {
	extractors := map[string]Extractor{}
	for _, extractor := range []Extractor{
		goExtractor{}, terraformExtractor{}, bashExtractor{}, csharpExtractor{},
		rustExtractor{}, rExtractor{}, yamlExtractor{"yaml"}, yamlExtractor{"helm"},
		yamlExtractor{"ansible"}, pythonRegexExtractor{},
	} {
		extractors[extractor.Language()] = extractor
	}
	return extractors
}

// envPatterns find environment variable reads per language.
var envPatterns = map[string]*regexp.Regexp{
	"go":        regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("([A-Za-z_][A-Za-z0-9_]*)"\)`),
	"python":    regexp.MustCompile(`os\.(?:getenv\(|environ\.get\(|environ\[)\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`),
	"bash":      regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)(?::?[-=?+])`),
	"csharp":    regexp.MustCompile(`Environment\.GetEnvironmentVariable\(\s*"([A-Za-z_][A-Za-z0-9_]*)"`),
	"rust":      regexp.MustCompile(`env::var(?:_os)?\(\s*"([A-Za-z_][A-Za-z0-9_]*)"`),
	"r":         regexp.MustCompile(`Sys\.getenv\(\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`),
	"ansible":   regexp.MustCompile(`lookup\(\s*['"]env['"]\s*,\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]`),
	"helm":      regexp.MustCompile(`\benv\s+"([A-Za-z_][A-Za-z0-9_]*)"`),
	"terraform": regexp.MustCompile(`\bTF_VAR_([A-Za-z_][A-Za-z0-9_]*)`),
}

// EnvVars finds environment variable reads in a file.
func EnvVars(language, path string, source []byte) []EnvVar {
	pattern, ok := envPatterns[language]
	if !ok {
		return nil
	}
	var found []EnvVar
	seen := map[string]bool{}
	for number, line := range strings.Split(string(source), "\n") {
		for _, match := range pattern.FindAllStringSubmatch(line, -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				found = append(found, EnvVar{Name: match[1], File: path, Line: number + 1})
			}
		}
	}
	return found
}

// commentBlock collects the comment lines directly above line index (0-based), using
// prefix to recognise them (e.g. "#", "///", "#'").
func commentBlock(lines []string, index int, prefixes ...string) string {
	var block []string
	for current := index - 1; current >= 0; current-- {
		trimmed := strings.TrimSpace(lines[current])
		matched := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(trimmed, prefix) {
				text := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
				if strings.HasPrefix(text, "!") { // shebangs
					return strings.Join(block, "\n")
				}
				block = append([]string{text}, block...)
				matched = true
				break
			}
		}
		if !matched {
			break
		}
	}
	return strings.TrimSpace(strings.Join(block, "\n"))
}

func isUpper(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}
