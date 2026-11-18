package gen

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/JB0t/docgen/internal/specs"
)

// taskKeywords are the keys of a task that are not its module.
var taskKeywords = toSet(`name when register loop loop_control become become_user become_method become_flags
tags vars notify delegate_to delegate_facts run_once ignore_errors ignore_unreachable changed_when failed_when
until retries delay environment no_log args check_mode diff any_errors_fatal async poll throttle timeout
collections connection debugger module_defaults listen port remote_user block rescue always local_action action
with_items with_dict with_list with_fileglob with_together with_subelements with_sequence with_nested
with_first_found with_indexed_items with_flattened with_random_choice with_lines with_inventory_hostnames`)

func toSet(words string) map[string]bool {
	set := map[string]bool{}
	for _, word := range strings.Fields(words) {
		set[word] = true
	}
	return set
}

// yamlDocuments parses every document of a YAML file into nodes.
func yamlDocuments(content string) []*yaml.Node {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var documents []*yaml.Node
	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			break
		}
		if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
			documents = append(documents, node.Content[0])
		}
	}
	return documents
}

// mapping returns the key/value pairs of a mapping node.
func mapping(node *yaml.Node) [][2]*yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	var pairs [][2]*yaml.Node
	for index := 0; index+1 < len(node.Content); index += 2 {
		pairs = append(pairs, [2]*yaml.Node{node.Content[index], node.Content[index+1]})
	}
	return pairs
}

func get(node *yaml.Node, key string) *yaml.Node {
	for _, pair := range mapping(node) {
		if pair[0].Value == key {
			return pair[1]
		}
	}
	return nil
}

// isPlay reports whether a list item is a play rather than a task.
func isPlay(node *yaml.Node) bool {
	return get(node, "hosts") != nil || get(node, "import_playbook") != nil || get(node, "ansible.builtin.import_playbook") != nil
}

// ansibleFileKind classifies a YAML file: "playbook", "tasks", or "" (vars, meta, other).
func ansibleFileKind(content string) string {
	documents := yamlDocuments(content)
	if len(documents) == 0 || documents[0].Kind != yaml.SequenceNode || len(documents[0].Content) == 0 {
		return ""
	}
	first := documents[0].Content[0]
	if first.Kind != yaml.MappingNode {
		return ""
	}
	if isPlay(first) {
		return "playbook"
	}
	return "tasks"
}

// eachTask calls visit for every task of a playbook or task file, including tasks in
// blocks and handlers.
func eachTask(root *yaml.Node, visit func(task *yaml.Node)) {
	if root == nil || root.Kind != yaml.SequenceNode {
		return
	}
	var walkTasks func(list *yaml.Node)
	walkTasks = func(list *yaml.Node) {
		if list == nil || list.Kind != yaml.SequenceNode {
			return
		}
		for _, task := range list.Content {
			if task.Kind != yaml.MappingNode {
				continue
			}
			if get(task, "block") != nil {
				visit(task) // block-level keywords (when, vars) count too
				walkTasks(get(task, "block"))
				walkTasks(get(task, "rescue"))
				walkTasks(get(task, "always"))
				continue
			}
			visit(task)
		}
	}
	for _, item := range root.Content {
		if isPlay(item) {
			for _, section := range []string{"pre_tasks", "tasks", "post_tasks", "handlers"} {
				walkTasks(get(item, section))
			}
		} else {
			walkTasks(&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{item}})
		}
	}
}

// taskModule returns a task's module key and arguments.
func taskModule(task *yaml.Node) (*yaml.Node, *yaml.Node) {
	for _, pair := range mapping(task) {
		if !taskKeywords[pair[0].Value] && !strings.HasPrefix(pair[0].Value, "with_") {
			return pair[0], pair[1]
		}
	}
	return nil, nil
}

// moduleIndex finds module specs by full and short name.
type moduleIndex struct {
	full        map[string]*specs.Entry
	short       map[string]*specs.Entry
	sets        map[string]*specs.Set // by entry name
	collections map[string]*specs.Set // by set (collection) name
	shortNames  []string
}

func newModuleIndex(sets []*specs.Set) *moduleIndex {
	index := &moduleIndex{full: map[string]*specs.Entry{}, short: map[string]*specs.Entry{}, sets: map[string]*specs.Set{}, collections: map[string]*specs.Set{}}
	for _, set := range sets {
		if set.Kind != specs.KindAnsible {
			continue
		}
		index.collections[set.Name] = set
		for position := range set.Entries {
			entry := &set.Entries[position]
			index.full[entry.Name] = entry
			short := entry.Name[strings.LastIndex(entry.Name, ".")+1:]
			index.short[short] = entry
			index.shortNames = append(index.shortNames, short)
			index.sets[entry.Name] = set
		}
	}
	sort.Strings(index.shortNames)
	return index
}

// resolve finds the spec of a module key, or reports a module that cannot exist.
func (m *moduleIndex) resolve(name string) (*specs.Entry, string) {
	if entry, ok := m.full[name]; ok {
		return entry, ""
	}
	parts := strings.Split(name, ".")
	if len(parts) >= 3 {
		collection := parts[0] + "." + parts[1]
		set, known := m.collections[collection]
		if !known {
			return nil, "" // a collection without specs: not checked
		}
		var names []string
		for _, entry := range set.Entries {
			names = append(names, entry.Name[strings.LastIndex(entry.Name, ".")+1:])
		}
		message := fmt.Sprintf("there is no module %s in %s", name, collection)
		if closest := specs.Closest(parts[len(parts)-1], names); closest != "" {
			message += fmt.Sprintf(" (did you mean %s.%s?)", collection, closest)
		}
		return nil, message
	}
	if entry, ok := m.short[name]; ok {
		return entry, ""
	}
	// A short name that looks like a stored collection's module (same prefix) but is not one.
	prefix, _, found := strings.Cut(name, "_")
	if !found {
		return nil, ""
	}
	if closest := specs.Closest(name, m.shortNames); closest != "" && strings.HasPrefix(closest, prefix+"_") {
		return nil, fmt.Sprintf("there is no module %s (did you mean %s?)", name, m.short[closest].Name)
	}
	return nil, ""
}

// ansibleSpecCheck checks every task's module and options against the stored specs.
var ansibleSpecCheck = Check{Name: "ansible specs", Run: func(ctx *CheckContext) []Problem {
	index := newModuleIndex(ctx.Sets)
	if len(index.full) == 0 {
		return nil
	}
	var problems []Problem
	for _, file := range filesWith(ctx, ".yml", ".yaml") {
		for _, document := range yamlDocuments(ctx.Contents[file]) {
			eachTask(document, func(task *yaml.Node) {
				key, value := taskModule(task)
				if key == nil {
					return
				}
				entry, unknown := index.resolve(key.Value)
				if unknown != "" {
					problems = append(problems, Problem{File: file, Line: key.Line, Message: unknown})
				}
				if entry == nil {
					return
				}
				arguments := value
				if extra := get(task, "args"); extra != nil && extra.Kind == yaml.MappingNode {
					merged := &yaml.Node{Kind: yaml.MappingNode, Line: extra.Line}
					if value.Kind == yaml.MappingNode {
						merged.Content = append(merged.Content, value.Content...)
					}
					merged.Content = append(merged.Content, extra.Content...)
					arguments = merged
				}
				if arguments.Kind != yaml.MappingNode {
					return // free-form arguments (k=v) are not checked
				}
				set := index.sets[entry.Name]
				options := append(append([]specs.Option(nil), entry.Options...), set.Common...)
				for _, message := range checkOptions(shortModule(entry.Name), arguments, options, true) {
					problems = append(problems, Problem{File: file, Line: message.line, Message: message.text})
				}
			})
		}
	}
	return problems
}}

func shortModule(name string) string { return name[strings.LastIndex(name, ".")+1:] }

type lineMessage struct {
	line int
	text string
}

// checkOptions compares a mapping of arguments with the options of a module (or of
// one of its options), recursing into suboptions.
func checkOptions(owner string, arguments *yaml.Node, options []specs.Option, topLevel bool) []lineMessage {
	byName := map[string]*specs.Option{}
	var names []string
	for position := range options {
		option := &options[position]
		byName[option.Name] = option
		names = append(names, option.Name)
		for _, alias := range option.Aliases {
			byName[alias] = option
		}
	}
	var messages []lineMessage
	given := map[string]bool{}
	for _, pair := range mapping(arguments) {
		key, value := pair[0].Value, pair[1]
		option, ok := byName[key]
		if !ok {
			if strings.HasPrefix(key, "<<") { // YAML merge key
				continue
			}
			text := fmt.Sprintf("%s has no option %q", owner, key)
			if closest := specs.Closest(key, names); closest != "" {
				text += fmt.Sprintf(" (did you mean %q?)", closest)
			}
			messages = append(messages, lineMessage{pair[0].Line, text})
			continue
		}
		given[option.Name] = true
		if len(option.Choices) > 0 && value.Kind == yaml.ScalarNode && !strings.Contains(value.Value, "{{") && option.Type != "bool" && option.Type != "list" {
			if !containsFold(option.Choices, value.Value) {
				messages = append(messages, lineMessage{value.Line, fmt.Sprintf("%s: %s must be one of %s, not %q", owner, key, strings.Join(option.Choices, ", "), value.Value)})
			}
		}
		if len(option.Sub) > 0 {
			switch value.Kind {
			case yaml.MappingNode:
				messages = append(messages, checkOptions(owner+"."+key, value, option.Sub, false)...)
			case yaml.SequenceNode:
				for _, item := range value.Content {
					if item.Kind == yaml.MappingNode {
						messages = append(messages, checkOptions(owner+"."+key, item, option.Sub, false)...)
					}
				}
			}
		}
	}
	for _, option := range options {
		if option.Required && !given[option.Name] {
			messages = append(messages, lineMessage{arguments.Line, fmt.Sprintf("%s is missing the required option %q", owner, option.Name)})
		}
	}
	return messages
}

func containsFold(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

var roleFileRE = regexp.MustCompile(`(^|/)roles/[^/]+/`)

// ansibleSyntaxCheck runs ansible-playbook --syntax-check on generated playbooks, and
// on a small wrapper playbook for generated roles and task files, from the directory
// holding ansible.cfg (so roles_path and collections_path apply).
var ansibleSyntaxCheck = Check{Name: "ansible-playbook --syntax-check", Tool: "ansible-playbook", Run: func(ctx *CheckContext) []Problem {
	var problems []Problem
	type target struct{ file, wrapper string }
	var targets []target
	roles := map[string]bool{}
	for _, file := range filesWith(ctx, ".yml", ".yaml") {
		kind := ansibleFileKind(ctx.Contents[file])
		switch {
		case kind == "playbook":
			targets = append(targets, target{file: file})
		case roleFileRE.MatchString(file):
			location := roleFileRE.FindStringIndex(file)
			role := file[:location[1]-1]
			if !roles[role] {
				roles[role] = true
				targets = append(targets, target{file: role, wrapper: fmt.Sprintf("- hosts: localhost\n  gather_facts: false\n  roles:\n    - role: %s\n", ctx.Stage.Path(role))})
			}
		case kind == "tasks":
			targets = append(targets, target{file: file, wrapper: fmt.Sprintf("- hosts: localhost\n  gather_facts: false\n  tasks:\n    - ansible.builtin.import_tasks: %s\n", ctx.Stage.Path(file))})
		}
	}
	for number, item := range targets {
		dir := ansibleConfigDir(ctx.Stage, item.file)
		playbook := item.file
		if item.wrapper != "" {
			playbook = path.Join(dir, fmt.Sprintf(".docgen-syntax-check-%d.yml", number))
			if err := os.WriteFile(ctx.Stage.Path(playbook), []byte(item.wrapper), 0o644); err != nil {
				problems = append(problems, Problem{File: item.file, Message: err.Error()})
				continue
			}
		}
		relative, _ := filepath.Rel(ctx.Stage.Path(dir), ctx.Stage.Path(playbook))
		output, ok, err := ctx.Stage.Run(dir, toolTimeout, "ansible-playbook", "-i", "localhost,", "--syntax-check", relative)
		if item.wrapper != "" {
			_ = os.Remove(ctx.Stage.Path(playbook))
		}
		if err != nil {
			problems = append(problems, Problem{File: item.file, Message: err.Error()})
		} else if !ok {
			problems = append(problems, Problem{File: item.file, Message: "syntax check failed:\n" + trimOutput(stripWarnings(output))})
		}
	}
	return problems
}}

// ansibleConfigDir is the nearest directory above a file (in the stage) holding
// ansible.cfg, else the root.
func ansibleConfigDir(stage *Stage, file string) string {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if _, err := os.Stat(stage.Path(path.Join(dir, "ansible.cfg"))); err == nil {
			return dir
		}
		if dir == "." || dir == "/" {
			return "."
		}
	}
}

// stripWarnings drops Ansible's [WARNING] lines about empty inventories and the like.
func stripWarnings(output string) string {
	var kept []string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "[WARNING]") || strings.Contains(line, "provided hosts list is empty") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// ansibleLintCheck reports ansible-lint findings as advice: style rules are worth
// seeing but not worth another model round.
var ansibleLintCheck = Check{Name: "ansible-lint", Tool: "ansible-lint", Advice: true, Run: func(ctx *CheckContext) []Problem {
	files := filesWith(ctx, ".yml", ".yaml")
	if len(files) == 0 {
		return nil
	}
	args := append([]string{"--offline", "--nocolor", "-q", "-f", "pep8"}, files...)
	output, ok, err := ctx.Stage.Run(".", toolTimeout, "ansible-lint", args...)
	if err != nil || ok {
		return nil
	}
	var problems []Problem
	for _, line := range strings.Split(output, "\n") {
		for _, file := range files {
			if strings.HasPrefix(line, file+":") {
				problems = append(problems, Problem{Message: line})
				break
			}
		}
	}
	if len(problems) == 0 && strings.TrimSpace(output) != "" {
		problems = append(problems, Problem{Message: trimOutput(output)})
	}
	return problems
}}

// Input is a value that files from the context expect from whoever runs them: a
// playbook variable it uses but does not define, or a Terraform variable.
type Input struct {
	Name     string
	Optional bool   // has a default
	File     string // where it is used or declared
	// Structured marks a list or dict (the playbook loops over it): passing it as
	// -e name=value makes a string, so it must go in as JSON.
	Structured bool
}

var (
	loopVarRE    = regexp.MustCompile(`^\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*(?:\||\}\})`)
	jinjaRE      = regexp.MustCompile(`\{\{(.*?)\}\}|\{%(.*?)%\}`)
	identRE      = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	stringLitRE  = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	defaultRE    = regexp.MustCompile(`^\s*\|\s*(default|d)\b`)
	rawExprKeys  = toSet("when changed_when failed_when until that")
	magicNames   = toSet(`item hostvars groups group_names inventory_hostname inventory_hostname_short play_hosts playbook_dir role_path role_name inventory_dir inventory_file omit lookup query q range dict true false none True False None and or not in is if else elif endif for endfor loop environment vars now undef lambda`)
	setFactNames = toSet("set_fact ansible.builtin.set_fact ansible.legacy.set_fact set_stats ansible.builtin.set_stats")
)

// AnsibleInputs finds the variables Ansible files use without defining them: the
// values a caller (a workflow, a pipeline) has to pass with -e. Variables used only
// with | default(...) are optional. Defined are play and task vars, vars_prompt,
// register, set_fact, loop_var, and the keys of role defaults/ and vars/ files.
func AnsibleInputs(files map[string]string) []Input {
	defined := map[string]bool{}
	type use struct {
		file       string
		optional   bool
		structured bool
	}
	looped := map[string]bool{}
	uses := map[string]*use{}
	var order []string
	record := func(name, file string, optional bool) {
		if magicNames[name] || strings.HasPrefix(name, "ansible_") {
			return
		}
		if existing, ok := uses[name]; ok {
			existing.optional = existing.optional && optional
			return
		}
		uses[name] = &use{file: file, optional: optional}
		order = append(order, name)
	}
	scan := func(expression, file string) {
		expression = stringLitRE.ReplaceAllString(expression, `""`)
		locals := map[string]bool{}
		matches := identRE.FindAllStringIndex(expression, -1)
		for position, match := range matches {
			name := expression[match[0]:match[1]]
			before := strings.TrimRight(expression[:match[0]], " ")
			after := expression[match[1]:]
			if strings.HasSuffix(before, ".") || strings.HasSuffix(before, "|") || strings.HasPrefix(strings.TrimLeft(after, " "), "(") ||
				strings.HasPrefix(strings.TrimLeft(after, " "), "=") && !strings.HasPrefix(strings.TrimLeft(after, " "), "==") {
				continue
			}
			if position > 0 {
				previous := expression[matches[position-1][0]:matches[position-1][1]]
				if previous == "is" || previous == "not" && position > 1 && expression[matches[position-2][0]:matches[position-2][1]] == "is" || previous == "for" {
					if previous == "for" {
						locals[name] = true
					}
					continue
				}
			}
			if locals[name] {
				continue
			}
			record(name, file, defaultRE.MatchString(after))
		}
	}
	var walk func(node *yaml.Node, file, key string)
	walk = func(node *yaml.Node, file, key string) {
		switch node.Kind {
		case yaml.ScalarNode:
			if key == "loop" || strings.HasPrefix(key, "with_") {
				if match := loopVarRE.FindStringSubmatch(node.Value); match != nil {
					looped[match[1]] = true
				}
			}
			if rawExprKeys[key] && !strings.Contains(node.Value, "{{") {
				scan(node.Value, file)
				return
			}
			for _, match := range jinjaRE.FindAllStringSubmatch(node.Value, -1) {
				scan(match[1]+match[2], file)
			}
		case yaml.SequenceNode:
			for _, child := range node.Content {
				walk(child, file, key)
			}
		case yaml.MappingNode:
			for _, pair := range mapping(node) {
				walk(pair[1], file, pair[0].Value)
			}
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, file := range names {
		for _, document := range yamlDocuments(files[file]) {
			if document.Kind == yaml.MappingNode && (strings.Contains("/"+file, "/defaults/") || strings.Contains("/"+file, "/vars/")) {
				for _, pair := range mapping(document) {
					defined[pair[0].Value] = true
				}
			}
			if document.Kind != yaml.SequenceNode {
				continue
			}
			for _, item := range document.Content {
				if !isPlay(item) {
					continue
				}
				for _, pair := range mapping(get(item, "vars")) {
					defined[pair[0].Value] = true
				}
				if prompts := get(item, "vars_prompt"); prompts != nil {
					for _, prompt := range prompts.Content {
						if name := get(prompt, "name"); name != nil {
							defined[name.Value] = true
						}
					}
				}
			}
			eachTask(document, func(task *yaml.Node) {
				if register := get(task, "register"); register != nil {
					defined[register.Value] = true
				}
				for _, pair := range mapping(get(task, "vars")) {
					defined[pair[0].Value] = true
				}
				if loopVar := get(get(task, "loop_control"), "loop_var"); loopVar != nil {
					defined[loopVar.Value] = true
				}
				if key, value := taskModule(task); key != nil && setFactNames[key.Value] {
					for _, pair := range mapping(value) {
						defined[pair[0].Value] = true
					}
				}
			})
			walk(document, file, "")
		}
	}
	var inputs []Input
	for _, name := range order {
		if !defined[name] {
			inputs = append(inputs, Input{Name: name, Optional: uses[name].optional, File: uses[name].file, Structured: looped[name]})
		}
	}
	return inputs
}
