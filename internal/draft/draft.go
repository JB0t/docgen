// Package draft writes generated documentation sections into Markdown files.
//
// Each module (a directory of source files) gets an overview between docgen markers,
// written by a model from the module's facts and code, and, when docgen.yaml sets
// draft.reference, a reference list built directly from the facts (so it is always
// exact). Every overview is run through the checker before it is written; drafts that
// mention things the code does not contain are retried once with the problems listed,
// then rejected.
package draft

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JB0t/docgen/internal/check"
	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/extract"
	"github.com/JB0t/docgen/internal/facts"
	"github.com/JB0t/docgen/internal/llm"
)

// Options controls a draft run.
type Options struct {
	Scope         string // module directory, repo-relative
	Doc           string // target Markdown file; default <scope>/README.md
	Force         bool   // regenerate even when the sources are unchanged
	DryRun        bool   // print instead of writing
	MaxSourceToks int    // how much source code the model sees
	Reference     bool   // also write the reference list (docgen.yaml draft.reference)
	Log           func(format string, args ...any)
}

// Result reports what happened to one module.
type Result struct {
	Doc      string
	Sections []string // ids written
	Skipped  string   // reason, when nothing was written
	Findings []check.Finding
	// Fallbacks counts commands whose examples were replaced by the bare command.
	Fallbacks int
	Text      string // the new document (dry run)
}

const overviewSystem = `You write accurate developer documentation for one module of a code repository.

Write the module's overview: 1 to 3 short paragraphs on what the module is for, its main
concepts, and how its parts fit together. A reference list of every function and type is
generated separately, so do not list them all.

Rules:
- Use only what the facts and code show. Never invent behaviour, commands, flags, files,
  environment variables or configuration.
- Put code identifiers, paths and commands in backticks, and only ones that appear in the
  facts or code exactly as written.
- Plain Markdown paragraphs. No headings, no preamble, no closing remarks.
- Write caveman style: terse sentences, no articles, filler, hedging or pleasantries,
  one idea per sentence. Every name, path, command, flag and number stays exact.
  Example: "Scrapes each CLI's --help into facts. Draft writes sections from facts;
  check fails when docs name a flag the tool lacks."`

// Module drafts the sections for one module.
func Module(f *facts.Facts, cfg *config.Config, client *llm.Client, options Options) (Result, error) {
	scope := strings.TrimSuffix(filepath.ToSlash(options.Scope), "/")
	doc := options.Doc
	if doc == "" {
		doc = scope + "/README.md"
	}
	result := Result{Doc: doc}
	hash := f.FileHash(scope)
	if hash == "" {
		return result, fmt.Errorf("no files under %s", scope)
	}
	existing, _ := os.ReadFile(filepath.Join(f.Root, doc))
	text := string(existing)
	if text == "" {
		text = "# " + filepath.Base(scope) + "\n"
	}
	overviewID, referenceID := scope+":overview", scope+":reference"
	if !options.Force && current(text, scope, hash, options.Reference) {
		result.Skipped = "up to date"
		return result, nil
	}
	symbols := moduleSymbols(f, scope)
	if len(symbols) == 0 {
		result.Skipped = "no symbols to document"
		return result, nil
	}

	overview, findings, err := writeOverview(f, cfg, client, options, scope, doc, symbols)
	if err != nil {
		return result, err
	}
	result.Findings = findings
	if !options.Reference {
		if len(findings) > 0 {
			// Nothing exact to fall back on: leave the doc as it was (check reports a stale
			// overview) rather than delete text a reader may still use.
			result.Skipped = "overview failed checks; doc left as it was"
			return result, nil
		}
		text = upsert(text, overviewID, scope, hash, overview, true)
		text = remove(text, referenceID)
		result.Sections = []string{overviewID}
	} else {
		text = upsert(text, overviewID, scope, hash, overview, true)
		text = upsert(text, referenceID, scope, hash, reference(symbols, scope), false)
		result.Sections = []string{overviewID, referenceID}
		if len(findings) > 0 {
			// Keep the exact reference, but not an overview that failed checking.
			text = remove(text, overviewID)
			result.Sections = []string{referenceID}
		}
	}
	result.Text = text
	if options.DryRun {
		return result, nil
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(f.Root, doc)), 0o755); err != nil {
		return result, err
	}
	return result, os.WriteFile(filepath.Join(f.Root, doc), []byte(text), 0o644)
}

var testFileRE = regexp.MustCompile(`(^|/)(tests?/|testdata/|test_[^/]*\.py$|[^/]*_test\.(go|py)$|[^/]*Tests?\.cs$|[^/]*\.Tests?/)`)

// isTest reports whether a file only holds tests, which module docs leave out.
func isTest(path string) bool { return testFileRE.MatchString(path) }

// moduleSymbols returns exported symbols in non-test files under scope, in file order.
func moduleSymbols(f *facts.Facts, scope string) []extract.Symbol {
	var symbols []extract.Symbol
	for _, symbol := range f.Symbols {
		if facts.InScope(symbol.File, scope) && symbol.Exported && !isTest(symbol.File) {
			symbols = append(symbols, symbol)
		}
	}
	sort.SliceStable(symbols, func(i, j int) bool {
		if symbols[i].File != symbols[j].File {
			return symbols[i].File < symbols[j].File
		}
		return symbols[i].Line < symbols[j].Line
	})
	return symbols
}

func writeOverview(f *facts.Facts, cfg *config.Config, client *llm.Client, options Options, scope, doc string, symbols []extract.Symbol) (string, []check.Finding, error) {
	prompt := modulePrompt(f, scope, symbols, options.MaxSourceToks)
	messages := []llm.Message{{Role: "system", Content: overviewSystem}, {Role: "user", Content: prompt}}
	var findings []check.Finding
	for attempt := 1; attempt <= 2; attempt++ {
		answer, stats, err := client.Chat(messages, 1200)
		if err != nil {
			return "", nil, err
		}
		options.Log("  overview attempt %d: %s", attempt, stats)
		answer = cleanAnswer(answer)
		findings = problems(check.Text(f, cfg, doc, answer))
		if len(findings) == 0 {
			return answer, nil, nil
		}
		var list []string
		for _, finding := range findings {
			list = append(list, "- "+finding.Message)
		}
		options.Log("  overview attempt %d failed checks:\n    %s", attempt, strings.Join(list, "\n    "))
		messages = append(messages,
			llm.Message{Role: "assistant", Content: answer},
			llm.Message{Role: "user", Content: "These references do not exist in the code:\n" + strings.Join(list, "\n") +
				"\n\nRewrite the overview without them. Mention only identifiers, paths and commands that appear in the facts or code."})
	}
	return "", findings, nil
}

// Problems keeps the findings that mean a generated text claims something untrue
// (names, paths, flags or commands the code does not contain).
func Problems(findings []check.Finding) []check.Finding { return problems(findings) }

// problems keeps the findings that mean the draft claims something untrue.
func problems(findings []check.Finding) []check.Finding {
	var kept []check.Finding
	for _, finding := range findings {
		if finding.Kind != "stale" && finding.Kind != "coverage" && finding.Severity != check.Info {
			kept = append(kept, finding)
		}
	}
	return kept
}

var fenceAround = regexp.MustCompile("(?s)^```(?:markdown|md)?\\s*\\n(.*)\\n```\\s*$")

// cleanAnswer strips code fences and headings models sometimes wrap answers in.
func cleanAnswer(answer string) string {
	answer = strings.TrimSpace(answer)
	if match := fenceAround.FindStringSubmatch(answer); match != nil {
		answer = strings.TrimSpace(match[1])
	}
	var lines []string
	for _, line := range strings.Split(answer, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") && len(lines) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// modulePrompt gives the model the module's facts and as much source as fits.
func modulePrompt(f *facts.Facts, scope string, symbols []extract.Symbol, maxSource int) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "MODULE: %s\n\nFILES:\n", scope)
	var files []facts.File
	for _, file := range f.Files {
		if facts.InScope(file.Path, scope) && !isTest(file.Path) {
			files = append(files, file)
			fmt.Fprintf(&builder, "- %s (%s, %d lines)\n", file.Path, file.Language, file.Lines)
		}
	}
	builder.WriteString("\nEXPORTED SYMBOLS:\n")
	for _, symbol := range symbols {
		name := symbol.Name
		if symbol.Parent != "" {
			name = symbol.Parent + "." + symbol.Name
		}
		fmt.Fprintf(&builder, "- %s %s (%s)", symbol.Kind, name, symbol.File)
		if symbol.Signature != "" {
			fmt.Fprintf(&builder, ": %s", symbol.Signature)
		}
		if doc := firstLine(symbol.Doc); doc != "" {
			fmt.Fprintf(&builder, " - %s", doc)
		}
		builder.WriteString("\n")
	}
	var env []string
	seen := map[string]bool{}
	for _, variable := range f.EnvVars {
		if facts.InScope(variable.File, scope) && !seen[variable.Name] {
			seen[variable.Name] = true
			env = append(env, variable.Name)
		}
	}
	if len(env) > 0 {
		fmt.Fprintf(&builder, "\nENVIRONMENT VARIABLES READ: %s\n", strings.Join(env, ", "))
	}
	// Source: smaller files first, so more files fit; documentation-heavy entry points
	// (package docs) tend to be small anyway.
	sort.SliceStable(files, func(i, j int) bool { return files[i].Lines < files[j].Lines })
	builder.WriteString("\nSOURCE:\n")
	remaining := maxSource
	for _, file := range files {
		if file.Language == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.Root, file.Path))
		if err != nil || len(data) > remaining {
			continue
		}
		remaining -= len(data)
		fmt.Fprintf(&builder, "\n--- %s\n%s\n", file.Path, data)
	}
	builder.WriteString("\nWrite the overview.")
	return builder.String()
}

// reference lists exported symbols per file with signature and first doc line.
func reference(symbols []extract.Symbol, scope string) string {
	var builder strings.Builder
	builder.WriteString("## Reference\n")
	currentFile := ""
	for _, symbol := range symbols {
		if symbol.Kind == "package" || symbol.Kind == "module" || symbol.Kind == "script" && symbol.Doc == "" {
			continue
		}
		if symbol.File != currentFile {
			currentFile = symbol.File
			rel := strings.TrimPrefix(strings.TrimPrefix(symbol.File, scope), "/")
			if rel == "" {
				rel = filepath.Base(symbol.File)
			}
			fmt.Fprintf(&builder, "\n### `%s`\n\n", rel)
		}
		name := symbol.Name
		if symbol.Parent != "" {
			name = symbol.Parent + "." + symbol.Name
		}
		line := fmt.Sprintf("- **`%s`** (%s)", name, symbol.Kind)
		if doc := firstLine(symbol.Doc); doc != "" {
			line += ": " + escapeHTML(doc)
		}
		builder.WriteString(line + "\n")
	}
	return strings.TrimSpace(builder.String())
}

var inlineCodeRE = regexp.MustCompile("`[^`]*`")

// escapeHTML stops <placeholders> in doc comments being read as HTML tags, outside
// inline code where Markdown already shows them literally.
func escapeHTML(text string) string {
	var builder strings.Builder
	last := 0
	for _, span := range inlineCodeRE.FindAllStringIndex(text, -1) {
		builder.WriteString(strings.ReplaceAll(text[last:span[0]], "<", "&lt;"))
		builder.WriteString(text[span[0]:span[1]])
		last = span[1]
	}
	builder.WriteString(strings.ReplaceAll(text[last:], "<", "&lt;"))
	return builder.String()
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.Index(text, "\n\n"); index >= 0 {
		text = text[:index]
	}
	return strings.Join(strings.Fields(text), " ")
}

func markerRE(id string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)<!--\s*docgen:begin\s+id=` + regexp.QuoteMeta(id) + `(\s[^>]*)?-->.*?<!--\s*docgen:end\s+id=` + regexp.QuoteMeta(id) + `\s*-->\n?`)
}

var hashAttr = regexp.MustCompile(`hash=(\S+)`)

// current reports whether a module's generated sections match its sources: the
// overview (or, where only a reference survived failed checks, the reference), and the
// reference too when references are on.
func current(text, scope, hash string, reference bool) bool {
	if recordedHash(text, scope) != hash {
		return false
	}
	return !reference || sectionHash(text, scope+":reference") == hash
}

// recordedHash is the source hash a module's generated sections were written from: the
// overview's, else the reference's, else "" (no generated sections).
func recordedHash(text, scope string) string {
	if hash := sectionHash(text, scope+":overview"); hash != "" {
		return hash
	}
	return sectionHash(text, scope+":reference")
}

// sectionHash returns the hash recorded on a section's begin marker, or "".
func sectionHash(text, id string) string {
	match := markerRE(id).FindString(text)
	if found := hashAttr.FindStringSubmatch(match); found != nil {
		return found[1]
	}
	return ""
}

// upsert replaces the section with this id, or inserts it: overviews right after the
// first heading (and any intro paragraph a human wrote), references at the end.
func upsert(text, id, scope, hash, body string, near bool) string {
	section := fmt.Sprintf("<!-- docgen:begin id=%s scope=%s hash=%s -->\n%s\n<!-- docgen:end id=%s -->\n", id, scope, hash, strings.TrimSpace(body), id)
	pattern := markerRE(id)
	if pattern.MatchString(text) {
		return pattern.ReplaceAllLiteralString(text, section)
	}
	if !near {
		return strings.TrimRight(text, "\n") + "\n\n" + section
	}
	lines := strings.Split(text, "\n")
	insertAt := 0
	for index, line := range lines {
		if strings.HasPrefix(line, "# ") {
			insertAt = index + 1
			break
		}
	}
	before := strings.TrimRight(strings.Join(lines[:insertAt], "\n"), "\n")
	after := strings.TrimLeft(strings.Join(lines[insertAt:], "\n"), "\n")
	return before + "\n\n" + section + "\n" + after
}

func remove(text, id string) string {
	return markerRE(id).ReplaceAllString(text, "")
}

func shortHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:6])
}

// upsertBefore replaces the section with this id, or inserts it before the first
// section whose id ends in ":"+beforeSuffix (e.g. the reference list), else appends.
func upsertBefore(text, id, scope, hash, body, beforeSuffix string) string {
	if markerRE(id).MatchString(text) {
		return upsert(text, id, scope, hash, body, false)
	}
	section := fmt.Sprintf("<!-- docgen:begin id=%s scope=%s hash=%s -->\n%s\n<!-- docgen:end id=%s -->\n", id, scope, hash, strings.TrimSpace(body), id)
	marker := regexp.MustCompile(`<!--\s*docgen:begin\s+id=\S+:` + regexp.QuoteMeta(beforeSuffix) + `\s`)
	if location := marker.FindStringIndex(text); location != nil {
		return text[:location[0]] + section + "\n" + text[location[0]:]
	}
	return strings.TrimRight(text, "\n") + "\n\n" + section
}

// ModuleInfo is a directory worth documenting and the state of its generated sections.
type ModuleInfo struct {
	Scope   string
	Symbols int
	State   string // new (no generated sections yet), stale or up to date
}

// Modules finds directories under root (repo-relative, "" for all) that contain
// exported, non-test symbols, in path order. Each is one directory (not its subdirectories).
func Modules(f *facts.Facts, root string) []ModuleInfo {
	root = strings.TrimSuffix(filepath.ToSlash(root), "/")
	counts := map[string]int{}
	for _, symbol := range f.Symbols {
		if !symbol.Exported || isTest(symbol.File) || symbol.Kind == "package" || symbol.Kind == "module" {
			continue
		}
		if root != "" && root != "." && symbol.File != root && !strings.HasPrefix(symbol.File, root+"/") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(symbol.File))
		if dir == "." {
			continue // the repository root's README is the project's own front page
		}
		counts[dir]++
	}
	var modules []ModuleInfo
	for scope, count := range counts {
		info := ModuleInfo{Scope: scope, Symbols: count, State: "new"}
		data, err := os.ReadFile(filepath.Join(f.Root, scope, "README.md"))
		if err == nil {
			switch recorded := recordedHash(string(data), scope); {
			case recorded == "":
			case recorded == f.FileHash(scope):
				info.State = "up to date"
			default:
				info.State = "stale"
			}
		}
		modules = append(modules, info)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Scope < modules[j].Scope })
	return modules
}

// Section returns the body of a generated section (id "<scope>:<suffix>", e.g.
// "internal/notes:overview") from the module's README, or "" when there is none. It is how
// other commands reuse what draft already wrote instead of asking the model again.
func Section(root, scope, suffix string) string {
	scope = strings.TrimSuffix(filepath.ToSlash(scope), "/")
	data, err := os.ReadFile(filepath.Join(root, scope, "README.md"))
	if err != nil {
		return ""
	}
	match := markerRE(scope + ":" + suffix).FindString(string(data))
	if match == "" {
		return ""
	}
	// Drop the begin marker line and the end marker.
	if index := strings.Index(match, "-->\n"); index >= 0 {
		match = match[index+4:]
	}
	if index := strings.LastIndex(match, "<!--"); index >= 0 {
		match = match[:index]
	}
	return strings.TrimSpace(match)
}

// UpToDate reports, without the model, whether a module's sections match its sources;
// reference is docgen.yaml's draft.reference.
func UpToDate(f *facts.Facts, scope, doc string, reference bool) bool {
	scope = strings.TrimSuffix(filepath.ToSlash(scope), "/")
	if doc == "" {
		doc = scope + "/README.md"
	}
	data, err := os.ReadFile(filepath.Join(f.Root, doc))
	if err != nil {
		return false
	}
	hash := f.FileHash(scope)
	return hash != "" && current(string(data), scope, hash, reference)
}
