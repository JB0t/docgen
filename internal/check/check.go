// Package check validates documentation against the facts: every command, flag,
// path, symbol and environment variable a doc mentions must exist, generated sections
// must have intact markers, and generated sections whose sources changed are stale.
package check

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/facts"
)

type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
	Info    Severity = "info"
)

// Finding is one problem in one doc.
type Finding struct {
	Doc      string   `json:"doc"`
	Line     int      `json:"line"`
	Severity Severity `json:"severity"`
	Kind     string   `json:"kind"` // command, flag, path, symbol, env, marker, stale, coverage
	Ref      string   `json:"ref"`
	Message  string   `json:"message"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", f.Doc, f.Line, f.Severity, f.Message)
}

var (
	inlineCodeRE = regexp.MustCompile("`([^`\n]+)`")
	fenceRE      = regexp.MustCompile("^\\s*(```+|~~~+)\\s*([\\w+-]*)")
	beginRE      = regexp.MustCompile(`<!--\s*docgen:begin\s+(.*?)\s*-->`)
	endRE        = regexp.MustCompile(`<!--\s*docgen:end\s+id=(\S+)\s*-->`)
	attrRE       = regexp.MustCompile(`(\w+)=(\S+)`)
	flagRE       = regexp.MustCompile(`^(--?[A-Za-z][\w-]*)(?:=.*)?$`)
	envRE        = regexp.MustCompile(`^\$?\{?([A-Z][A-Z0-9]*_[A-Z0-9_]+|[A-Z]{3,})\}?$`)
	symbolRE     = regexp.MustCompile(`^[A-Za-z_][\w]*(?:[.:]{1,2}[A-Za-z_]\w*)*(\(\))?$`)
	fileExtRE    = regexp.MustCompile(`\.[A-Za-z0-9]{1,6}$`)
	promptRE     = regexp.MustCompile(`^\s*(?:\$|>|#)\s+`)
)

// fileExtensions marks spans like `config.py` as file names rather than symbols.
var fileExtensions = map[string]bool{}

func init() {
	for _, ext := range strings.Fields(".md .go .py .sh .bash .json .yaml .yml .toml .tf .tfvars .txt .cs .csproj .sln .rs .r .rmd .lock .mod .sum .cfg .ini .conf .csv .sql .html .js .ts .env .xml .ps1 .hcl .j2 .tpl") {
		fileExtensions[ext] = true
	}
}

var shellFences = map[string]bool{"": true, "bash": true, "sh": true, "shell": true, "console": true, "zsh": true, "terminal": true}

// Options control what is checked.
type Options struct {
	Coverage bool // also report commands, flags and env vars no doc mentions
}

type checker struct {
	facts    *facts.Facts
	cfg      *config.Config
	root     string
	findings []Finding
	ignore   map[string]bool
	clis     map[string]facts.CLI
	// mentioned collects what the docs reference, for coverage.
	mentionedCommands map[string]bool
	mentionedWords    map[string]bool
	paths             map[string]bool // every repo file and directory, built on first use
	ignored           map[string]bool // git check-ignore answers, by absolute path
}

// Run checks every doc in the facts.
func Run(f *facts.Facts, cfg *config.Config, options Options) []Finding {
	c := newChecker(f, cfg)
	for _, doc := range f.Docs {
		data, err := os.ReadFile(filepath.Join(c.root, doc.Path))
		if err == nil {
			c.doc(doc.Path, string(data))
		}
	}
	if options.Coverage {
		c.coverage()
	}
	sort.SliceStable(c.findings, func(i, j int) bool {
		if c.findings[i].Doc != c.findings[j].Doc {
			return c.findings[i].Doc < c.findings[j].Doc
		}
		return c.findings[i].Line < c.findings[j].Line
	})
	return c.findings
}

// Text checks Markdown that is not a file yet (e.g. a generated draft). path is where
// it will be written, which relative paths are resolved against.
func Text(f *facts.Facts, cfg *config.Config, path, text string) []Finding {
	c := newChecker(f, cfg)
	c.doc(path, text)
	return c.findings
}

func newChecker(f *facts.Facts, cfg *config.Config) *checker {
	c := &checker{facts: f, cfg: cfg, root: f.Root, ignore: map[string]bool{}, clis: map[string]facts.CLI{},
		mentionedCommands: map[string]bool{}, mentionedWords: map[string]bool{}, ignored: map[string]bool{}}
	for _, ignored := range cfg.Ignore {
		c.ignore[ignored] = true
	}
	for _, cli := range f.CLIs {
		c.clis[cli.Name] = cli
	}
	return c
}

func (c *checker) add(doc string, line int, severity Severity, kind, ref, format string, args ...any) {
	if c.ignore[ref] {
		return
	}
	c.findings = append(c.findings, Finding{Doc: doc, Line: line, Severity: severity, Kind: kind, Ref: ref, Message: fmt.Sprintf(format, args...)})
}

func (c *checker) doc(path, text string) {
	lines := strings.Split(text, "\n")
	fence, fenceLang := "", ""
	open := map[string]int{} // marker id -> line
	for index, line := range lines {
		number := index + 1
		if match := fenceRE.FindStringSubmatch(line); match != nil {
			if fence == "" {
				fence, fenceLang = match[1], strings.ToLower(match[2])
			} else if strings.HasPrefix(strings.TrimSpace(line), fence) {
				fence = ""
			}
			continue
		}
		if fence != "" {
			if shellFences[fenceLang] {
				c.shellLine(path, number, line)
			}
			continue
		}
		c.markers(path, number, line, open)
		for _, match := range inlineCodeRE.FindAllStringSubmatch(line, -1) {
			c.inline(path, number, strings.TrimSpace(match[1]))
		}
	}
	for id, line := range open {
		c.add(path, line, Error, "marker", id, "generated section %q has no matching docgen:end marker", id)
	}
}

// markers checks generated-section markers: matched pairs, and not stale.
func (c *checker) markers(path string, number int, line string, open map[string]int) {
	if match := beginRE.FindStringSubmatch(line); match != nil {
		attributes := map[string]string{}
		for _, pair := range attrRE.FindAllStringSubmatch(match[1], -1) {
			attributes[pair[1]] = pair[2]
		}
		id := attributes["id"]
		if id == "" {
			c.add(path, number, Error, "marker", line, "docgen:begin marker without an id")
			return
		}
		open[id] = number
		if scope, hash := attributes["scope"], attributes["hash"]; scope != "" && hash != "" {
			if current := c.facts.FileHash(scope); current == "" {
				c.add(path, number, Error, "stale", id, "generated section %q documents %s, which no longer exists", id, scope)
			} else if current != hash {
				c.add(path, number, Warning, "stale", id, "generated section %q is stale: %s changed since it was generated", id, scope)
			}
		}
	}
	if match := endRE.FindStringSubmatch(line); match != nil {
		if _, ok := open[match[1]]; !ok {
			c.add(path, number, Error, "marker", match[1], "docgen:end for %q without a matching begin", match[1])
		}
		delete(open, match[1])
	}
}

// shellLine checks a command line inside a shell code block.
func (c *checker) shellLine(path string, number int, line string) {
	line = promptRE.ReplaceAllString(line, "")
	if index := strings.Index(line, " #"); index >= 0 {
		line = line[:index]
	}
	words := shellWords(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "\\")))
	if len(words) == 0 || strings.HasPrefix(words[0], "#") {
		return
	}
	// Skip leading VAR=value assignments and wrappers.
	for len(words) > 0 && (strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "-") || words[0] == "sudo" || words[0] == "time") {
		words = words[1:]
	}
	if len(words) == 0 {
		return
	}
	if cli, ok := c.clis[words[0]]; ok {
		c.command(path, number, cli, words[1:])
		c.alternatives(path, number, cli, words, line)
		return
	}
	// ./mytool or bin/mytool: the same CLI invoked by path. Check its arguments, and
	// whether that path is right.
	if cli, ok := c.clis[filepath.Base(words[0])]; ok && strings.Contains(words[0], "/") {
		c.command(path, number, cli, words[1:])
		c.cliPath(path, number, cli, words[0])
		return
	}
	if strings.HasPrefix(words[0], "./") || strings.Contains(words[0], "/") && !strings.HasPrefix(words[0], "/") && !strings.HasPrefix(words[0], "~") && !strings.Contains(words[0], "$") {
		c.path(path, number, words[0])
	}
}

// command checks a CLI invocation: the subcommand path, its flags, and whether it
// takes positional arguments at all. Flags may come before the subcommand, and the
// value of a value-taking flag is skipped.
func (c *checker) command(path string, number int, cli facts.CLI, args []string) {
	commandPath := ""
	resolving := true
	var flags, positionals []string
	for index := 0; index < len(args); index++ {
		if args[index] == "--" {
			break // everything after -- belongs to something else (e.g. a container command)
		}
		// Usage notation: [optional] parts, {a|b} choices and "..." repeats.
		arg := strings.Trim(args[index], "[]{}")
		if arg == "" || arg == "..." || strings.HasSuffix(arg, "...") {
			continue
		}
		if match := flagRE.FindStringSubmatch(arg); match != nil {
			flags = append(flags, match[1])
			// A group's action flag runs one of its subcommands (`tasks -l` is `tasks
			// list`): the flags after it are that subcommand's.
			if child, ok := cli.Action(commandPath, match[1]); ok && resolving {
				commandPath = child
				continue
			}
			if value, ok := strings.CutPrefix(arg, match[1]+"="); ok {
				c.flagValue(path, number, cli, commandPath, match[1], value)
			} else if cli.TakesValue(commandPath, match[1]) {
				index++ // its value
				if index < len(args) {
					c.flagValue(path, number, cli, commandPath, match[1], args[index])
				}
			}
			continue
		}
		if resolving {
			candidate := strings.TrimSpace(commandPath + " " + arg)
			if child, ok := cli.Child(commandPath, arg); ok {
				commandPath = child
				continue
			}
			// Hidden commands and deprecated names are not in the help pages: ask the
			// tool itself.
			if (hasChildren(cli, commandPath) || cli.ListsSubcommands(commandPath)) && len(positionals) == 0 && commandWordRE.MatchString(arg) && cli.WrappedBy(commandPath) == "" {
				if found, ok := cli.Probe(candidate); ok {
					if _, known := cli.Find(found.Path); !known {
						cli.Commands = append(cli.Commands, found)
						c.clis[cli.Name] = cli
					}
					commandPath = found.Path
					continue
				}
			}
			// Not a subcommand: a positional argument, unless the command has children
			// and this looks like a misspelt or removed subcommand.
			if hasChildren(cli, commandPath) && len(positionals) == 0 && symbolRE.MatchString(arg) && !strings.Contains(arg, ".") {
				c.add(path, number, Error, "command", cli.Name+" "+candidate, "`%s %s` is not a command (no such subcommand %q)", cli.Name, candidate, arg)
				return
			}
			resolving = false
		}
		positionals = append(positionals, arg)
	}
	c.mentionedCommands[commandPath] = true
	if command, ok := cli.Find(commandPath); ok && command.NoArgs && len(positionals) > 0 {
		c.add(path, number, Error, "command", cli.Name+" "+commandPath, "`%s %s` takes no positional arguments, but is given %q", cli.Name, commandPath, positionals[0])
	}
	for _, flag := range flags {
		c.mentionedWords[flag] = true
		if !cli.HasFlag(commandPath, flag) {
			c.add(path, number, Error, "flag", flag, "`%s %s` has no flag %s", cli.Name, commandPath, flag)
		}
	}
}

// commandWordRE matches words that may name a subcommand (start-timer, refresh).
var commandWordRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$`)

var (
	intRE      = regexp.MustCompile(`^-?\d+$`)
	floatRE    = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
	dateRE     = regexp.MustCompile(`^\d{4}[-/]\d{2}[-/]\d{2}$`)
	durationRE = regexp.MustCompile(`^\d+[smhdw]$`)
)

// flagValue catches values that cannot be right: non-numbers for numeric flags, dates
// or paths given as IDs, and non-dates given to date flags. Placeholders such as
// <id>, ID or $VAR are not judged.
func (c *checker) flagValue(path string, number int, cli facts.CLI, commandPath, flag, value string) {
	value = strings.Trim(value, `"'`)
	if value == "" || strings.ContainsAny(value, "<>$") || strings.ToUpper(value) == value && !intRE.MatchString(value) && !strings.Contains(value, "-") {
		return
	}
	name := strings.TrimLeft(flag, "-")
	problem := ""
	switch kind := cli.ValueType(commandPath, flag); {
	case kind == "int" || kind == "uint" || kind == "int64" || kind == "uint64" || kind == "int32":
		if !intRE.MatchString(value) {
			problem = "an integer"
		}
	case kind == "float" || kind == "float64" || kind == "float32":
		if !floatRE.MatchString(value) {
			problem = "a number"
		}
	case name == "id" || strings.HasSuffix(name, "-id") || strings.HasSuffix(name, "_id"):
		if dateRE.MatchString(value) || strings.Contains(value, "/") {
			problem = "an ID, not a date or path"
		}
	case name == "date" || strings.HasSuffix(name, "-date"):
		if !dateRE.MatchString(value) && !durationRE.MatchString(value) && value != "today" && value != "yesterday" && value != "tomorrow" {
			problem = "a date"
		}
	}
	if problem != "" {
		c.add(path, number, Error, "value", flag+" "+value, "`%s %s %s %s`: %s expects %s", cli.Name, commandPath, flag, value, flag, problem)
	}
}

// alternatives handles docs that list sibling commands as `tool group a | b | c`:
// when every pipe-separated part after the first is a single word, each is checked
// as a sibling of the first command instead of being treated as a pipeline.
func (c *checker) alternatives(path string, number int, cli facts.CLI, words []string, line string) {
	parts := strings.Split(line, "|")
	if len(parts) < 2 || len(words) < 2 {
		return
	}
	parent := strings.Join(words[1:len(words)-1], " ")
	anySibling := false
	for _, part := range parts[1:] {
		fields := strings.Fields(part)
		if len(fields) != 1 || strings.HasPrefix(fields[0], "-") {
			return // a real pipeline
		}
		if _, ok := cli.Find(strings.TrimSpace(parent + " " + fields[0])); ok {
			anySibling = true
		}
	}
	if !anySibling {
		return // `tool cmd | less` pipes into another program
	}
	for _, part := range parts[1:] {
		c.command(path, number, cli, append(strings.Fields(parent), strings.Fields(part)...))
	}
}

func hasChildren(cli facts.CLI, path string) bool {
	prefix := path + " " // "alert " must not match "alert-provider"
	for _, command := range cli.Commands {
		if command.Path != path && (path == "" || strings.HasPrefix(command.Path, prefix)) {
			return true
		}
	}
	return false
}

// inline classifies an inline code span and checks it.
func (c *checker) inline(path string, number int, span string) {
	if c.ignore[span] || strings.Contains(span, "://") || strings.ContainsAny(span, "<>*{}") && !envRE.MatchString(span) {
		return
	}
	words := shellWords(span)
	if len(words) > 1 {
		if cli, ok := c.clis[words[0]]; ok {
			c.command(path, number, cli, words[1:])
			c.alternatives(path, number, cli, words, span)
		}
		return
	}
	switch {
	case flagRE.MatchString(span):
		flag := flagRE.FindStringSubmatch(span)[1]
		c.mentionedWords[flag] = true
		if !c.knownFlag(flag) {
			c.add(path, number, Warning, "flag", flag, "flag %s does not appear in any CLI help or in the code", flag)
		}
	case envRE.MatchString(span):
		name := envRE.FindStringSubmatch(span)[1]
		c.mentionedWords[name] = true
		if !c.facts.HasEnvVar(name) && !c.facts.Mentioned(name) && !c.facts.Mentioned("$"+name) && !c.builtName(name) {
			c.add(path, number, Warning, "env", name, "environment variable %s is not read anywhere in the code", name)
		}
	case strings.Contains(span, "/") || fileExtRE.MatchString(span) && fileExtensions[strings.ToLower(fileExtRE.FindString(span))]:
		c.path(path, number, span)
	case symbolRE.MatchString(span) && looksLikeSymbol(span):
		name := strings.TrimSuffix(span, "()")
		c.mentionedWords[name] = true
		last := name
		if index := strings.LastIndexAny(name, ".:"); index >= 0 {
			last = name[index+1:]
		}
		if !c.facts.HasSymbol(name) && !c.facts.HasSymbol(last) && !c.facts.Mentioned(name) && !c.facts.Mentioned(last) {
			c.add(path, number, Warning, "symbol", name, "%s is not defined or used anywhere in the code", span)
		}
	}
}

// builtName reports whether the code may build an environment variable's name from
// two parts it mentions: a prefix ending in _ and the rest, as in
// `prefix=LLM_DIGEST_` and `${prefix}THREADS`.
func (c *checker) builtName(name string) bool {
	for index := strings.Index(name, "_"); index >= 0 && index < len(name)-1; {
		if c.facts.Mentioned(name[:index+1]) && c.facts.Mentioned(name[index+1:]) {
			return true
		}
		next := strings.Index(name[index+1:], "_")
		if next < 0 {
			break
		}
		index += next + 1
	}
	return false
}

func (c *checker) knownFlag(flag string) bool {
	for _, cli := range c.facts.CLIs {
		for _, command := range cli.Commands {
			for _, known := range command.Flags {
				if known == flag {
					return true
				}
			}
		}
	}
	return c.facts.HasSymbol(flag) || c.facts.Mentioned(flag)
}

// looksLikeSymbol keeps checks to spans that are clearly code identifiers: calls,
// dotted names, snake_case or camelCase/PascalCase, not ordinary words.
func looksLikeSymbol(span string) bool {
	if strings.HasSuffix(span, "()") || strings.ContainsAny(span, ".:") {
		return true
	}
	if strings.Contains(span, "_") {
		return true
	}
	upper := 0
	for index, char := range span {
		if char >= 'A' && char <= 'Z' && index > 0 {
			upper++
		}
	}
	return upper > 0 && span != strings.ToUpper(span)
}

// path checks that a repo path a doc mentions exists, relative to the doc or the root.
func (c *checker) path(doc string, number int, ref string) {
	if imageRE.MatchString(ref) {
		return // a container image (registry/name:tag), not a file
	}
	ref = lineSuffixRE.ReplaceAllString(ref, "") // file.go:12
	ref = strings.TrimSuffix(ref, "/")
	if ref == "" || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "~") || strings.ContainsAny(ref, "$%*:") || strings.HasPrefix(ref, "-") {
		return
	}
	c.mentionedWords[ref] = true
	clean := strings.TrimPrefix(ref, "./")
	// .git/ is git's own directory: whatever a doc names there is written at runtime.
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return
	}
	// `.tfvars` or `.tftest.hcl` names a kind of file, not a file.
	if !strings.Contains(clean, "/") && strings.HasPrefix(clean, ".") && fileExtensions[strings.ToLower(filepath.Ext(clean))] {
		return
	}
	repoName := filepath.Base(c.root)
	bases := []string{filepath.Dir(filepath.Join(c.root, doc)), c.root}
	if strings.HasPrefix(clean, repoName+"/") || clean == repoName {
		bases = append(bases, filepath.Dir(c.root)) // "dev-lab/apps/x" from inside dev-lab
	}
	for _, base := range bases {
		if _, err := os.Stat(filepath.Join(base, clean)); err == nil {
			return
		}
	}
	// A bare name or partial path may mean "that file, wherever it is": accept it if
	// any path in the repository ends with it.
	if c.repoHasSuffix(clean) {
		return
	}
	// Local files kept out of git on purpose (configs, token caches) and files the code
	// writes or reads at runtime (it names them) are real, just not in the repository.
	if c.gitIgnored(bases[:2], clean) || c.namedInCode(clean) {
		return
	}
	// Only report what is clearly meant as a repository path: explicitly relative, or
	// starting with a directory that exists. Anything else (Blue/green, alpine/kubectl,
	// output files) is prose or external.
	first := strings.Split(clean, "/")[0]
	_, firstErr := os.Stat(filepath.Join(c.root, first))
	_, firstNearErr := os.Stat(filepath.Join(filepath.Dir(filepath.Join(c.root, doc)), first))
	switch {
	case strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../"):
		c.add(doc, number, Error, "path", ref, "path %s does not exist", ref)
	case strings.Contains(clean, "/") && (firstErr == nil || firstNearErr == nil):
		// With a file extension it is surely a file; without one it may be a name that
		// only looks like a path (a ci/... branch, a group/name).
		severity := Warning
		if filepath.Ext(clean) != "" {
			severity = Error
		}
		c.add(doc, number, severity, "path", ref, "path %s does not exist (the %s/ directory does)", ref, first)
	case !strings.Contains(clean, "/") && fileExtensions[strings.ToLower(filepath.Ext(clean))]:
		c.add(doc, number, Warning, "path", ref, "no file named %s anywhere in the repository", ref)
	}
}

var (
	// Container images: a registry host (with a dot or port) first, a :tag at the end,
	// or a digest.
	imageRE      = regexp.MustCompile(`^[\w.-]+\.[\w.-]+(:\d+)?/|^[\w.-]+(/[\w.-]+)*:[\w][\w.-]*$|@sha256:`)
	lineSuffixRE = regexp.MustCompile(`:\d+(:\d+)?$`)
)

// gitIgnored reports whether git ignores the path, relative to any of the bases.
func (c *checker) gitIgnored(bases []string, path string) bool {
	for _, base := range bases {
		full := filepath.Join(base, path)
		ignored, ok := c.ignored[full]
		if !ok {
			command := exec.Command("git", "-C", base, "check-ignore", "-q", "--", path)
			ignored = command.Run() == nil // exit 1: not ignored; 128: not in a repository
			c.ignored[full] = ignored
		}
		if ignored {
			return true
		}
	}
	return false
}

// namedInCode reports whether the code names the file: the whole path, or, for a bare
// file name, the name (`"status.md"` in a filepath.Join).
func (c *checker) namedInCode(path string) bool {
	return c.facts.Mentioned(path) || !strings.Contains(path, "/") && c.facts.Mentioned(filepath.Base(path))
}

// repoHasSuffix reports whether any file or directory in the repository is name or
// ends with /name.
func (c *checker) repoHasSuffix(name string) bool {
	if c.paths == nil {
		c.paths = map[string]bool{}
		for _, file := range c.facts.Files {
			for path := file.Path; path != "." && path != ""; path = filepath.ToSlash(filepath.Dir(path)) {
				c.paths[path] = true
			}
		}
	}
	if c.paths[name] {
		return true
	}
	for path := range c.paths {
		if strings.HasSuffix(path, "/"+name) {
			return true
		}
	}
	return false
}

// cliPath checks a path a doc uses to run a CLI (./devlab). When it is wrong relative
// to the doc and the repository root but the CLI's own directory has it, that is
// probably an assumed `cd`, so it is a warning naming the real location.
func (c *checker) cliPath(doc string, number int, cli facts.CLI, ref string) {
	clean := strings.TrimPrefix(ref, "./")
	for _, base := range []string{filepath.Dir(filepath.Join(c.root, doc)), c.root} {
		if _, err := os.Stat(filepath.Join(base, clean)); err == nil {
			return
		}
	}
	if cli.Dir != "" {
		dir := cli.Dir
		if filepath.IsAbs(dir) {
			if rel, err := filepath.Rel(c.root, dir); err == nil {
				dir = rel
			}
		}
		if _, err := os.Stat(filepath.Join(c.root, dir, clean)); err == nil {
			c.add(doc, number, Warning, "path", ref, "%s is in %s/, not here: say where to run it from, or use %s", ref, dir, filepath.ToSlash(filepath.Join(dir, clean)))
			return
		}
	}
	c.add(doc, number, Error, "path", ref, "path %s does not exist", ref)
}

// coverage reports CLI commands and env vars that no doc mentions.
func (c *checker) coverage() {
	for _, cli := range c.facts.CLIs {
		for _, command := range cli.Commands {
			if command.Path != "" && !c.mentionedCommands[command.Path] {
				c.add("(coverage)", 0, Info, "coverage", cli.Name+" "+command.Path, "`%s %s` is not shown in any doc", cli.Name, command.Path)
			}
		}
	}
	seen := map[string]bool{}
	for name := range systemEnv {
		seen[name] = true
	}
	for _, env := range c.facts.EnvVars {
		if !seen[env.Name] && !c.mentionedWords[env.Name] {
			seen[env.Name] = true
			c.add("(coverage)", 0, Info, "coverage", env.Name, "environment variable %s (read in %s) is not documented", env.Name, env.File)
		}
	}
}

// systemEnv are variables every program may read; not worth documenting per tool.
var systemEnv = map[string]bool{"HOME": true, "USER": true, "PATH": true, "SHELL": true, "TERM": true, "PWD": true,
	"LANG": true, "TMPDIR": true, "TMUX": true, "NO_COLOR": true, "LOGNAME": true, "HOSTNAME": true}

// shellWords splits a command line, keeping quoted strings together.
func shellWords(line string) []string {
	var words []string
	var current strings.Builder
	quote := rune(0)
	for _, char := range line {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
		case char == '"' || char == '\'':
			quote = char
		case char == ' ' || char == '\t':
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		case char == '|' || char == ';' || char == '&':
			if current.Len() > 0 {
				words = append(words, current.String())
			}
			return words // only the first command of a pipeline is checked
		default:
			current.WriteRune(char)
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}
