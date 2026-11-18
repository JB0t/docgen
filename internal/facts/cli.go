package facts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JB0t/docgen/internal/config"
)

// CLI is a command-line tool's command tree, scraped from its --help output.
type CLI struct {
	Name     string    `json:"name"`
	Dir      string    `json:"dir,omitempty"` // repo-relative directory of its source
	Doc      string    `json:"doc,omitempty"` // where its usage section goes
	Commands []Command `json:"commands"`

	// prober runs the tool for Probe; nil when the tree did not come from a real tool.
	prober *prober
}

// Command is one (sub)command: its path below the tool ("" for the root, "tasks list").
type Command struct {
	Path  string   `json:"path"`
	Short string   `json:"short,omitempty"`
	Flags []string `json:"flags"`
	// ValueFlags are the flags that take a value (`--config string`, `--workspace NAME`).
	ValueFlags []string `json:"value_flags,omitempty"`
	// ValueTypes maps a flag to its documented value type ("int", "string", ...).
	ValueTypes map[string]string `json:"value_types,omitempty"`
	// NoArgs is true when the usage line shows no positional arguments.
	NoArgs bool `json:"no_args,omitempty"`
	// Aliases are other names the command answers to ("pods" for "pod").
	Aliases []string `json:"aliases,omitempty"`
	// Wraps names the program whose help this command shows when it passes its
	// arguments through to another tool (`devlab kubectl` -> "kubectl").
	Wraps string `json:"wraps,omitempty"`
	// Actions maps a group's boolean action flags to the subcommand each one runs:
	// `clctl tasks -l` is `clctl tasks list` when the help lists `-l, --list` and `list`.
	Actions map[string]string `json:"actions,omitempty"`
	Help    string            `json:"help,omitempty"` // the full --help output
}

var (
	// Headings like "Available Commands:", "Basic Commands (Beginner):", "options:".
	sectionRE = regexp.MustCompile(`^([A-Za-z][A-Za-z ()/&,'-]*):\s*$`)
	// kubectl-style tools list their global flags on a separate page:
	// Use "kubectl options" for a list of global command-line options.
	globalOptionsRE = regexp.MustCompile(`Use "([^"]+) options" for a list of global`)
	// kubectl-style flag definitions: "-A, --all-namespaces=false:" with the
	// description on the next line; the default tells booleans from value flags.
	defaultFlagRE = regexp.MustCompile(`^((?:-[A-Za-z0-9], )?--[A-Za-z][\w-]*)=(\S*):$`)
	flagRE        = regexp.MustCompile(`(?:^|[\s,\[])(--?[A-Za-z][\w-]*)`)
	choicesRE     = regexp.MustCompile(`^\s*\{([\w,-]+)\}`)
	// Commands are listed as "  name   Short": cobra pads names to the longest one, which
	// is followed by a single space, so one space is enough.
	entryRE = regexp.MustCompile(`^\s{1,6}([A-Za-z][\w:-]*)(?:\s+(.*))?$`)
)

// flagSpecRE matches the flag definition at the start of a help line, before the
// description: "-o, --output string", "--dry-run", "--workspace WORKSPACE".
// cobra writes optional values as `--select string[=" "]`, hence the [=...] part.
// click and argparse also write choices as `--profile [local|ci]` or `{a,b}`.
var flagSpecRE = regexp.MustCompile(`^((?:--?[A-Za-z][\w-]*(?: / --?[A-Za-z][\w-]*)?(?:[ =](?:[A-Za-z][\w\[\]<>.|-]*(?:\[=[^\]]*\])?|\[[^\]]*\]|\{[^}]*\}|<[^>]*>))?(?:,\s*)?)+)(?:\s{2,}|$)`)

// flagSections are help headings whose lines define flags.
var flagSections = map[string]bool{"flags": true, "global flags": true, "options": true, "optional arguments": true, "": true}

// isFlagSection also accepts headings such as "The following options can be passed
// to any command" (kubectl options).
func isFlagSection(section string) bool {
	return flagSections[section] || (strings.Contains(section, "option") || strings.Contains(section, "flag")) && !strings.Contains(section, "commands")
}

// flagLines returns the flag definitions in a help text's flag sections, one per line.
func flagLines(help string) []string {
	var specs []string
	section := ""
	for _, line := range strings.Split(help, "\n") {
		if match := sectionRE.FindStringSubmatch(line); match != nil {
			section = strings.ToLower(strings.TrimSpace(match[1]))
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !isFlagSection(section) || !strings.HasPrefix(trimmed, "-") || !strings.HasPrefix(line, " ") {
			continue
		}
		if match := defaultFlagRE.FindStringSubmatch(trimmed); match != nil {
			// Normalise to the usual "flags type" form: booleans have no value.
			if match[2] == "true" || match[2] == "false" {
				specs = append(specs, match[1])
			} else {
				specs = append(specs, match[1]+" "+defaultType(match[2]))
			}
			continue
		}
		if match := flagSpecRE.FindStringSubmatch(trimmed); match != nil {
			specs = append(specs, strings.TrimSpace(match[1]))
		}
	}
	return specs
}

// defaultType guesses a value type from a kubectl-style default (”, 0, [], 'x').
func defaultType(value string) string {
	if intRE.MatchString(value) {
		return "int"
	}
	return "string"
}

var intRE = regexp.MustCompile(`^-?\d+$`)

// commandSections are help-output headings that list subcommands, across cobra
// ("Available Commands"), click ("Commands") and argparse ("positional arguments").
var commandSections = map[string]bool{"available commands": true, "commands": true, "positional arguments": true, "subcommands": true}

// isCommandSection also accepts grouped headings such as "Deploy Commands" or
// "Basic Commands (Beginner)".
func isCommandSection(section string) bool {
	return commandSections[section] || strings.Contains(section, "commands") && !strings.Contains(section, "global")
}

// ScrapeCLI runs the tool's --help recursively (three levels deep) and records each
// command's flags. `go run` tools are built once first so scraping stays fast.
func ScrapeCLI(root string, cli config.CLI) (CLI, error) {
	run := append([]string(nil), cli.Run...)
	if len(run) == 0 {
		run = []string{cli.Name}
	}
	dir := cli.Dir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return CLI{}, fmt.Errorf("CLI %s: dir %s is not a directory", cli.Name, dir)
	}
	if len(run) >= 3 && run[0] == "go" && run[1] == "run" {
		temp, err := os.MkdirTemp("", "docgen-cli")
		if err != nil {
			return CLI{}, err
		}
		defer os.RemoveAll(temp)
		binary := filepath.Join(temp, cli.Name)
		build := exec.Command("go", "build", "-o", binary, run[2])
		build.Dir = dir
		if output, err := build.CombinedOutput(); err != nil {
			return CLI{}, fmt.Errorf("building %s: %v\n%s", cli.Name, err, output)
		}
		run = append([]string{binary}, run[3:]...)
	}

	page := func(path []string, help bool) (string, error) { return helpPage(run, dir, cli.Env, path, help) }

	// Read the command tree level by level, several help pages at a time. Each node
	// gets a sort key from its parent's listing order, so the result reads like the
	// tool's own help (depth first).
	type node struct {
		path  []string
		key   string
		short string
	}
	type result struct {
		command     Command
		key         string
		subcommands []subcommand
		err         error
	}
	var commands []Command
	keys := map[string]string{}
	globalPages := map[string]bool{}
	level := []node{{path: nil, key: ""}}
	for depth := 0; len(level) > 0; depth++ {
		results := make([]result, len(level))
		var group sync.WaitGroup
		limiter := make(chan struct{}, 8)
		for index, item := range level {
			group.Add(1)
			go func() {
				defer group.Done()
				limiter <- struct{}{}
				defer func() { <-limiter }()
				help, err := page(item.path, true)
				if err != nil {
					results[index] = result{err: err}
					return
				}
				command, subcommands := commandFromHelp(help, cli.Name, item.path, item.short)
				results[index] = result{key: item.key, subcommands: subcommands, command: command}
				// A wrapped tool keeping its global flags on an "options" page.
				if match := globalOptionsRE.FindStringSubmatch(help); match != nil && len(item.path) > 0 {
					results[index].command.Help += "\n\x00options:" + match[1]
				}
			}()
		}
		group.Wait()
		var next []node
		for index, out := range results {
			if out.err != nil {
				return CLI{}, out.err
			}
			command := out.command
			if marker := strings.Index(command.Help, "\n\x00options:"); marker >= 0 {
				tool := command.Help[marker+len("\n\x00options:"):]
				command.Help = command.Help[:marker]
				if !globalPages[tool] {
					globalPages[tool] = true
					// Global flags belong to this command so its subcommands inherit them.
					if text, err := page(append(append([]string(nil), level[index].path...), "options"), false); err == nil {
						command.Flags = appendNew(command.Flags, flagNames(text)...)
						for flag, kind := range valueTypes(text) {
							if command.ValueTypes == nil {
								command.ValueTypes = map[string]string{}
							}
							command.ValueTypes[flag] = kind
							command.ValueFlags = appendNew(command.ValueFlags, flag)
						}
					}
				}
			}
			commands = append(commands, command)
			keys[command.Path] = out.key
			if depth >= 3 {
				continue
			}
			for position, sub := range out.subcommands {
				if sub.name == "help" {
					continue
				}
				next = append(next, node{
					path:  append(append([]string(nil), level[index].path...), sub.name),
					key:   fmt.Sprintf("%s/%04d", out.key, position),
					short: sub.short,
				})
			}
		}
		level = next
	}
	sort.SliceStable(commands, func(i, j int) bool { return keys[commands[i].Path] < keys[commands[j].Path] })
	return CLI{Name: cli.Name, Dir: filepath.ToSlash(cli.Dir), Doc: cli.Doc, Commands: commands}, nil
}

// helpPage runs the tool with path and returns its output. Help pages get --help;
// "options" pages of wrapped tools (kubectl options) print their flags without it.
func helpPage(run []string, dir string, env map[string]string, path []string, help bool) (string, error) {
	args := append(append([]string(nil), run[1:]...), path...)
	if help {
		args = append(args, "--help")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, run[0], args...)
	command.Dir = dir
	// Keep the tool from prompting or colouring its output.
	command.Env = append(os.Environ(), "NO_COLOR=1", "TERM=dumb")
	for key, value := range env {
		command.Env = append(command.Env, key+"="+os.ExpandEnv(value))
	}
	output, runErr := command.CombinedOutput() // many tools exit non-zero after --help
	if strings.TrimSpace(string(output)) == "" {
		reason := "it printed nothing"
		if runErr != nil {
			reason = runErr.Error()
		}
		return "", fmt.Errorf("running `%s %s` in %s: %s", strings.Join(run, " "), strings.Join(args[len(run)-1:], " "), dir, reason)
	}
	return string(output), nil
}

// commandFromHelp reads one help page into the command at path, and the subcommands
// it lists.
func commandFromHelp(help, name string, path []string, short string) (Command, []subcommand) {
	subcommands, flags := parseHelp(help)
	return Command{
		Path: strings.Join(path, " "), Short: short, Flags: flags,
		ValueFlags: valueFlags(help), ValueTypes: valueTypes(help),
		NoArgs: noArgs(help, name, path), Aliases: aliases(help), Wraps: wrappedProgram(help, name, path),
		Actions: actionFlags(help, path, subcommands), Help: strings.TrimSpace(help),
	}, subcommands
}

// actionFlags maps each boolean flag named after a listed subcommand (`-l, --list` for
// `list`) to that subcommand's path, for every spelling on its line.
func actionFlags(help string, path []string, subcommands []subcommand) map[string]string {
	names := map[string]bool{}
	for _, sub := range subcommands {
		names[sub.name] = true
	}
	actions := map[string]string{}
	for _, spec := range flagLines(help) {
		if flagValueRE.MatchString(spec) {
			continue // takes a value: an option, not an action
		}
		spellings := flagRE.FindAllStringSubmatch(spec, -1)
		for _, spelling := range spellings {
			name, long := strings.CutPrefix(spelling[1], "--")
			if !long || !names[name] {
				continue
			}
			for _, each := range spellings {
				actions[each[1]] = strings.Join(append(append([]string(nil), path...), name), " ")
			}
		}
	}
	if len(actions) == 0 {
		return nil
	}
	return actions
}

// Action returns the subcommand a boolean action flag of the command at path runs.
func (c CLI) Action(path, flag string) (string, bool) {
	command, ok := c.Find(path)
	if !ok {
		return "", false
	}
	child, ok := command.Actions[flag]
	return child, ok
}

// prober asks a tool about commands its help pages do not list, remembering answers.
type prober struct {
	run  []string
	dir  string
	env  map[string]string
	mu   sync.Mutex
	seen map[string]*Command // path -> the command, or nil when it is not one
}

func newProber(root string, cli config.CLI) *prober {
	run := append([]string(nil), cli.Run...)
	if len(run) == 0 {
		run = []string{cli.Name}
	}
	dir := cli.Dir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return &prober{run: run, dir: dir, env: cli.Env, seen: map[string]*Command{}}
}

// cobraHelpRE is the last line of every cobra help page with subcommands.
var cobraHelpRE = regexp.MustCompile(`\[command\] --help" for more information about a command`)

// Probe asks the tool whether path ("tasks start-timer") is a command its help pages
// do not list: a hidden command or a deprecated name. It returns that command, under
// its real path. Only cobra tools are asked: cobra answers --help with a help page
// and never runs the command, whatever the arguments before it.
func (c CLI) Probe(path string) (Command, bool) {
	root, ok := c.Find("")
	if c.prober == nil || !ok || !cobraHelpRE.MatchString(root.Help) {
		return Command{}, false
	}
	p := c.prober
	p.mu.Lock()
	defer p.mu.Unlock()
	if found, ok := p.seen[path]; ok {
		if found == nil {
			return Command{}, false
		}
		return *found, true
	}
	p.seen[path] = nil
	words := strings.Fields(path)
	help, err := helpPage(p.run, p.dir, p.env, words, true)
	if err != nil {
		return Command{}, false
	}
	// The help page is the command's own when its usage line names it, or names the
	// command it is an alias of. Otherwise cobra showed the parent's page.
	parent := strings.Join(words[:len(words)-1], " ")
	for _, real := range usagePaths(help, c.Name) {
		realParent := ""
		if index := strings.LastIndex(real, " "); index >= 0 {
			realParent = real[:index]
		}
		if real != path && (realParent != parent || !contains(aliases(help), words[len(words)-1])) {
			continue
		}
		command, _ := commandFromHelp(help, c.Name, strings.Fields(real), "")
		p.seen[path] = &command
		return command, true
	}
	return Command{}, false
}

// ListsSubcommands reports whether the help page of the command at path lists
// subcommands: true for a probed hidden group, whose children are not in the tree.
func (c CLI) ListsSubcommands(path string) bool {
	command, ok := c.Find(path)
	if !ok {
		return false
	}
	subcommands, _ := parseHelp(command.Help)
	return len(subcommands) > 0
}

// usagePaths returns the command paths on a help page's usage lines
// ("clctl tasks list [flags]" -> "tasks list").
func usagePaths(help, name string) []string {
	var paths []string
	lines := strings.Split(help, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "Usage:" {
			continue
		}
		for _, usage := range lines[index+1:] {
			fields := strings.Fields(usage)
			if len(fields) == 0 {
				break
			}
			if fields[0] != name {
				continue
			}
			var path []string
			for _, word := range fields[1:] {
				if strings.ContainsAny(word, "[]<>{}") || strings.HasPrefix(word, "-") || strings.ToUpper(word) == word {
					break
				}
				path = append(path, word)
			}
			paths = append(paths, strings.Join(path, " "))
		}
		break
	}
	return paths
}

func contains(list []string, item string) bool {
	for _, each := range list {
		if each == item {
			return true
		}
	}
	return false
}

// flagNames returns the flags defined in a help text's flag sections.
func flagNames(help string) []string {
	_, flags := parseHelp(help)
	return flags
}

func appendNew(list []string, items ...string) []string {
	seen := map[string]bool{}
	for _, item := range list {
		seen[item] = true
	}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			list = append(list, item)
		}
	}
	return list
}

type subcommand struct{ name, short string }

// parseHelp finds subcommands (in a commands section) and every flag mentioned.
func parseHelp(help string) ([]subcommand, []string) {
	var subcommands []subcommand
	seenFlags := map[string]bool{}
	var flags []string
	section := ""
	for _, line := range strings.Split(help, "\n") {
		if match := sectionRE.FindStringSubmatch(line); match != nil {
			section = strings.ToLower(strings.TrimSpace(match[1]))
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "-") {
			continue // flags are collected from the flag sections below
		}
		if !isCommandSection(section) {
			continue
		}
		if match := choicesRE.FindStringSubmatch(line); match != nil { // argparse {a,b,c}
			for _, name := range strings.Split(match[1], ",") {
				subcommands = appendUnique(subcommands, subcommand{name: name})
			}
			continue
		}
		if match := entryRE.FindStringSubmatch(line); match != nil && (match[2] != "" || section != "positional arguments") {
			subcommands = appendUnique(subcommands, subcommand{name: match[1], short: strings.TrimSpace(match[2])})
		}
	}
	for _, spec := range flagLines(help) {
		for _, match := range flagRE.FindAllStringSubmatch(spec, -1) {
			if !seenFlags[match[1]] {
				seenFlags[match[1]] = true
				flags = append(flags, match[1])
			}
		}
	}
	return subcommands, flags
}

// flagValueRE finds "--flag type" (one space, then a value placeholder) in help lines;
// descriptions follow after two or more spaces, so boolean flags do not match.
var flagValueRE = regexp.MustCompile(`(--?[A-Za-z][\w-]*)[ =]([A-Za-z][\w<>.|-]*|\[[^\]]*\]|\{[^}]*\}|<[^>]*>)(?:\[[^\]]*\])?(?:,|\s|$)`)

// valueFlags returns flags whose definition shows a value placeholder. When one
// spelling on a line takes a value (`-n, --limit int`), they all do.
func valueFlags(help string) []string {
	var flags []string
	for flag := range valueTypes(help) {
		flags = append(flags, flag)
	}
	sort.Strings(flags)
	return flags
}

// valueTypes maps each value-taking flag to the placeholder its definition shows
// (`--limit int` -> "int"). Only flag definitions count, never descriptions.
func valueTypes(help string) map[string]string {
	types := map[string]string{}
	for _, spec := range flagLines(help) {
		match := flagValueRE.FindStringSubmatch(spec)
		if match == nil {
			continue
		}
		for _, flag := range flagRE.FindAllStringSubmatch(spec, -1) {
			types[flag[1]] = match[2]
		}
	}
	if len(types) == 0 {
		return nil
	}
	return types
}

// ValueType returns a flag's value type for the command or a parent, or "".
func (c CLI) ValueType(path, flag string) string {
	for {
		if command, ok := c.Find(path); ok {
			if kind, found := command.ValueTypes[flag]; found {
				return kind
			}
		}
		if path == "" {
			return ""
		}
		if index := strings.LastIndex(path, " "); index >= 0 {
			path = path[:index]
		} else {
			path = ""
		}
	}
}

// noArgs reports whether the usage line ("Usage:\n  tool sub [flags]") shows no
// positional arguments. Unknown layouts are assumed to take arguments.
func noArgs(help, name string, path []string) bool {
	prefix := strings.TrimSpace(name + " " + strings.Join(path, " "))
	lines := strings.Split(help, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "Usage:" {
			continue
		}
		found := false
		for _, usage := range lines[index+1:] {
			usage = strings.TrimSpace(usage)
			if usage == "" {
				break
			}
			if !strings.HasPrefix(usage, prefix) {
				continue
			}
			found = true
			for _, word := range strings.Fields(strings.TrimPrefix(usage, prefix)) {
				if word != "[flags]" && word != "[command]" {
					return false
				}
			}
		}
		return found
	}
	return false
}

// TakesValue reports whether a flag of the command (or a parent) takes a value.
func (c CLI) TakesValue(path, flag string) bool {
	for {
		if command, ok := c.Find(path); ok {
			for _, known := range command.ValueFlags {
				if known == flag {
					return true
				}
			}
		}
		if path == "" {
			return false
		}
		if index := strings.LastIndex(path, " "); index >= 0 {
			path = path[:index]
		} else {
			path = ""
		}
	}
}

func appendUnique(list []subcommand, item subcommand) []subcommand {
	for index, existing := range list {
		if existing.name == item.name {
			if existing.short == "" {
				list[index].short = item.short
			}
			return list
		}
	}
	return append(list, item)
}

// wrappedProgram returns the program named on a help page's usage line when it is not
// this CLI: the page is another tool's own help, passed through by a wrapper command.
func wrappedProgram(help, name string, path []string) string {
	if len(path) == 0 {
		return ""
	}
	program := ""
	lines := strings.Split(help, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if !strings.HasPrefix(lower, "usage:") {
			continue
		}
		rest := strings.TrimSpace(trimmed[len("usage:"):])
		if rest == "" && index+1 < len(lines) {
			rest = strings.TrimSpace(lines[index+1])
		}
		if fields := strings.Fields(rest); len(fields) > 0 {
			program = filepath.Base(fields[0])
		}
		break
	}
	if program == "" || program == name || strings.HasPrefix(program, "[") {
		return ""
	}
	return program
}

// WrappedBy returns the wrapper command (e.g. "kubectl") that path sits under, or "".
func (c CLI) WrappedBy(path string) string {
	for _, command := range c.Commands {
		if command.Wraps != "" && (path == command.Path || strings.HasPrefix(path, command.Path+" ")) {
			if parent := c.parentWrapper(command.Path); parent != "" {
				return parent
			}
			return command.Path
		}
	}
	return ""
}

// parentWrapper finds the outermost wrapper above path.
func (c CLI) parentWrapper(path string) string {
	outer := ""
	for _, command := range c.Commands {
		if command.Wraps != "" && command.Path != path && strings.HasPrefix(path, command.Path+" ") {
			if outer == "" || len(command.Path) < len(outer) {
				outer = command.Path
			}
		}
	}
	return outer
}

// aliases reads a help page's "Aliases:" section: names separated by commas on the
// following line(s), indented (cobra) or not (kubectl).
func aliases(help string) []string {
	lines := strings.Split(help, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "Aliases:" {
			continue
		}
		var names []string
		for _, next := range lines[index+1:] {
			next = strings.TrimSpace(next)
			if next == "" {
				break
			}
			for _, name := range strings.Split(next, ",") {
				if name = strings.TrimSpace(name); name != "" && !strings.Contains(name, " ") {
					names = append(names, name)
				}
			}
		}
		return names
	}
	return nil
}

// Child returns the path of the subcommand of parent called word, by name or alias.
func (c CLI) Child(parent, word string) (string, bool) {
	candidate := strings.TrimSpace(parent + " " + word)
	if _, ok := c.Find(candidate); ok {
		return candidate, true
	}
	for _, command := range c.Commands {
		index := strings.LastIndex(command.Path, " ")
		if command.Path == "" || strings.TrimSpace(command.Path[:max(index, 0)]) != parent || index < 0 && parent != "" {
			continue
		}
		for _, alias := range command.Aliases {
			if alias == word {
				return command.Path, true
			}
		}
	}
	return "", false
}

// Find returns the command at path ("tasks list") or false.
func (c CLI) Find(path string) (Command, bool) {
	for _, command := range c.Commands {
		if command.Path == path {
			return command, true
		}
	}
	return Command{}, false
}

// HasFlag reports whether a command (or one of its parents, for inherited flags)
// accepts the flag.
func (c CLI) HasFlag(path, flag string) bool {
	for {
		if command, ok := c.Find(path); ok {
			for _, known := range command.Flags {
				if known == flag {
					return true
				}
			}
		}
		if path == "" {
			return false
		}
		if index := strings.LastIndex(path, " "); index >= 0 {
			path = path[:index]
		} else {
			path = ""
		}
	}
}
