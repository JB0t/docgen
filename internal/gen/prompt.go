package gen

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/JB0t/docgen/internal/code"
	"github.com/JB0t/docgen/internal/llm"
	"github.com/JB0t/docgen/internal/specs"
)

// Picked is a spec chosen for the prompt, rendered.
type Picked struct {
	specs.Hit
	Text     string
	Compact  bool
	Explicit bool
}

// PickSpecs renders explicit hits first, then ranked ones, until max automatic picks or
// the token budget is reached; an entry that does not fit in full is tried compact.
// Duplicates are dropped. It also returns the common-options notes of the sets used.
func PickSpecs(explicit, ranked []specs.Hit, maxAuto, budget int) ([]Picked, []string) {
	var picked []Picked
	seen := map[*specs.Entry]bool{}
	commons := map[*specs.Set]string{}
	var commonOrder []*specs.Set
	used := 0
	add := func(hit specs.Hit, isExplicit bool) bool {
		if seen[hit.Entry] {
			return false
		}
		extra := 0
		if _, ok := commons[hit.Set]; !ok {
			if note := specs.RenderCommon(hit.Set); note != "" {
				extra = llm.EstimateTokens(note)
			}
		}
		for _, compact := range []bool{false, true} {
			text := specs.Render(hit.Set, hit.Entry, compact)
			cost := llm.EstimateTokens(text) + extra
			if used+cost <= budget || isExplicit && !compact && used == 0 {
				used += cost
				seen[hit.Entry] = true
				if _, ok := commons[hit.Set]; !ok {
					commons[hit.Set] = specs.RenderCommon(hit.Set)
					commonOrder = append(commonOrder, hit.Set)
				}
				picked = append(picked, Picked{Hit: hit, Text: text, Compact: compact, Explicit: isExplicit})
				return true
			}
		}
		return false
	}
	for _, hit := range explicit {
		add(hit, true)
	}
	automatic := 0
	for _, hit := range ranked {
		if automatic >= maxAuto {
			break
		}
		if add(hit, false) {
			automatic++
		}
	}
	var notes []string
	for _, set := range commonOrder {
		if commons[set] != "" {
			notes = append(notes, commons[set])
		}
	}
	return picked, notes
}

// Plan is everything a generation prompt holds, for --plan and for the model.
type Plan struct {
	Kind     *Kind
	Request  string
	Out      []string // root-relative files or directories the model may write
	Specs    []Picked
	Commons  []string
	Examples map[string]string // path -> content
	Context  map[string]string
	Inputs   []Input
	Targets  map[string]string // existing files under Out, root-relative -> content
	Create   []string          // Out files that do not exist yet
	Omitted  []string          // files left out (too large, binary, over budget)
	System   string
	Prompt   string
}

// Tokens estimates the prompt's size.
func (p *Plan) Tokens() int { return llm.EstimateTokens(p.System) + llm.EstimateTokens(p.Prompt) }

// ReadFiles reads files and directories (relative to base, or absolute) as text,
// skipping hidden folders other than .github and .gitlab, binary files and files over
// maxBytes; skipped paths are returned too.
func ReadFiles(base string, paths []string, maxBytes int64) (map[string]string, []string, error) {
	files := map[string]string{}
	var skipped []string
	for _, given := range paths {
		full := given
		if !filepath.IsAbs(full) {
			full = filepath.Join(base, given)
		}
		info, err := os.Stat(full)
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s: %w", given, err)
		}
		if !info.IsDir() {
			content, ok := readText(full, maxBytes)
			if !ok {
				skipped = append(skipped, given)
				continue
			}
			files[filepath.ToSlash(given)] = content
			continue
		}
		err = filepath.WalkDir(full, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := entry.Name()
			if entry.IsDir() {
				if path != full && strings.HasPrefix(name, ".") && name != ".github" && name != ".gitlab" {
					return filepath.SkipDir
				}
				return nil
			}
			rel := filepath.ToSlash(filepath.Join(given, strings.TrimPrefix(path, full)))
			content, ok := readText(path, maxBytes)
			if !ok {
				skipped = append(skipped, rel)
				return nil
			}
			files[rel] = content
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return files, skipped, nil
}

func readText(path string, maxBytes int64) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", false
	}
	return string(data), true
}

// ContextInputs finds what the context files expect from a caller.
func ContextInputs(context map[string]string) []Input {
	ansible := map[string]string{}
	terraform := map[string]string{}
	for path, content := range context {
		switch filepath.Ext(path) {
		case ".yml", ".yaml":
			if ansibleFileKind(content) != "" || strings.Contains("/"+path, "/defaults/") || strings.Contains("/"+path, "/vars/") {
				ansible[path] = content
			}
		case ".tf":
			terraform[path] = content
		}
	}
	inputs := AnsibleInputs(ansible)
	return append(inputs, TerraformInputs(terraform)...)
}

// Build writes the system message and prompt.
func (p *Plan) Build() {
	var system strings.Builder
	fmt.Fprintf(&system, "You write %s as boilerplate for a developer who will review and finish them. Write complete, working files that do what the request asks.\n", p.Kind.Title)
	system.WriteString(p.Kind.Guide + "\n")
	system.WriteString(`- Use only what the reference specs show when they cover the task: never invent modules, resources, actions, options or inputs. If the request needs something the specs do not show, write your best attempt and mark it with a TODO comment.
- Values only the developer knows (names, IDs, regions, sizes) are variables or inputs, never made-up literals; never write secrets.
- Follow the conventions of the example files (naming, layout, comments) when there are any.
- Do not explain anything outside the files.`)
	p.System = system.String()

	var prompt strings.Builder
	if len(p.Specs) > 0 {
		prompt.WriteString("## Reference specs\n\n")
		for _, note := range p.Commons {
			prompt.WriteString(note + "\n")
		}
		for _, spec := range p.Specs {
			prompt.WriteString(spec.Text + "\n")
		}
	}
	if len(p.Examples) > 0 {
		prompt.WriteString("## Examples of this project's conventions (follow their style; do not copy their content)\n")
		for _, path := range sortedFiles(p.Examples) {
			prompt.WriteString(fileBlock(path, p.Examples[path]))
		}
		prompt.WriteString("\n")
	}
	if len(p.Context) > 0 {
		prompt.WriteString("## Context: existing files the request builds on (read only; do not change them)\n")
		for _, path := range sortedFiles(p.Context) {
			prompt.WriteString(fileBlock(path, p.Context[path]))
		}
		if len(p.Inputs) > 0 {
			var required, optional []string
			for _, input := range p.Inputs {
				if input.Optional {
					optional = append(optional, input.Name)
				} else {
					required = append(required, input.Name)
				}
			}
			if len(required) > 0 {
				prompt.WriteString("\nValues these files need from whoever runs them (pass every one): " + strings.Join(required, ", ") + "\n")
			}
			if len(optional) > 0 {
				prompt.WriteString("Optional values (they have defaults): " + strings.Join(optional, ", ") + "\n")
			}
		}
		prompt.WriteString("\n")
	}
	prompt.WriteString("## Files to write\n")
	fmt.Fprintf(&prompt, "Create or change files only under: %s\n", strings.Join(p.Out, ", "))
	if len(p.Create) > 0 {
		prompt.WriteString("Create (empty SEARCH part): " + strings.Join(p.Create, ", ") + "\n")
	}
	if len(p.Targets) > 0 {
		prompt.WriteString("These files exist; change them with SEARCH parts copied from them, or leave them alone:\n")
		for _, path := range sortedFiles(p.Targets) {
			prompt.WriteString(fileBlock(path, p.Targets[path]))
		}
	}
	prompt.WriteString("\n## Request\n" + strings.TrimSpace(p.Request) + "\n")
	prompt.WriteString(code.BlocksInstructions())
	example := "path/to/file"
	if len(p.Create) > 0 {
		example = p.Create[0]
	} else if len(p.Out) > 0 && p.Out[0] != "." {
		example = p.Out[0]
	}
	fmt.Fprintf(&prompt, "\nEvery block starts with its file path on the line before <<<<<<< SEARCH, for example:\n%s\n<<<<<<< SEARCH\n=======\n(the whole new file)\n>>>>>>> REPLACE\n", example)
	p.Prompt = prompt.String()
}

func fileBlock(path, content string) string {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return fmt.Sprintf("\n<file path=%q>\n%s</file>\n", path, content)
}

// Describe summarises the plan for the terminal.
func (p *Plan) Describe() string {
	var out strings.Builder
	fmt.Fprintf(&out, "Kind: %s\n", p.Kind.Name)
	fmt.Fprintf(&out, "Writes under: %s\n", strings.Join(p.Out, ", "))
	if len(p.Specs) == 0 {
		out.WriteString("Specs: none (add some with docgen spec add)\n")
	} else {
		out.WriteString("Specs:\n")
		for _, spec := range p.Specs {
			why := fmt.Sprintf("score %.2f", spec.Score)
			if spec.Meaning != 0 {
				why += fmt.Sprintf(", meaning %.2f, keywords %.2f", spec.Meaning, spec.Keyword)
			}
			if spec.Explicit {
				why = "asked for"
			}
			if spec.Compact {
				why += ", compact"
			}
			fmt.Fprintf(&out, "  %-55s %s (%d tokens)\n", spec.Entry.Name, why, llm.EstimateTokens(spec.Text))
		}
	}
	list := func(label string, files map[string]string) {
		if len(files) == 0 {
			return
		}
		fmt.Fprintf(&out, "%s:\n", label)
		for _, path := range sortedFiles(files) {
			fmt.Fprintf(&out, "  %-55s %d tokens\n", path, llm.EstimateTokens(files[path]))
		}
	}
	list("Examples", p.Examples)
	list("Context", p.Context)
	if len(p.Inputs) > 0 {
		var names []string
		for _, input := range p.Inputs {
			name := input.Name
			if input.Optional {
				name += " (optional)"
			}
			names = append(names, name)
		}
		fmt.Fprintf(&out, "Context inputs: %s\n", strings.Join(names, ", "))
	}
	list("Existing target files", p.Targets)
	if len(p.Create) > 0 {
		fmt.Fprintf(&out, "To create: %s\n", strings.Join(p.Create, ", "))
	}
	if len(p.Omitted) > 0 {
		sort.Strings(p.Omitted)
		fmt.Fprintf(&out, "Left out (binary, too large or over budget): %s\n", strings.Join(p.Omitted, ", "))
	}
	fmt.Fprintf(&out, "Prompt: about %d tokens\n", p.Tokens())
	return out.String()
}
