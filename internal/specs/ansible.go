package specs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// AnsibleModules lists the modules of a collection (ansible-doc -l -j COLLECTION): FQCN
// to short description. ansible-doc must find the collection (ANSIBLE_COLLECTIONS_PATH,
// ansible.cfg, or the default ~/.ansible/collections).
func AnsibleModules(collection string) (map[string]string, error) {
	output, err := runTool("ansible-doc", "-t", "module", "-l", "-j", collection)
	if err != nil {
		return nil, err
	}
	modules := map[string]string{}
	if err := json.Unmarshal(output, &modules); err != nil {
		return nil, fmt.Errorf("reading ansible-doc's module list: %w", err)
	}
	if len(modules) == 0 {
		return nil, fmt.Errorf("ansible-doc lists no modules for %s; install it first (ansible-galaxy collection install %s)", collection, collection)
	}
	return modules, nil
}

// AnsibleDocs runs ansible-doc -j for modules, a batch at a time, and returns their
// entries. progress is called after each batch with the number done so far.
func AnsibleDocs(modules []string, progress func(done int)) ([]Entry, error) {
	var entries []Entry
	const batch = 40
	for start := 0; start < len(modules); start += batch {
		end := min(start+batch, len(modules))
		args := append([]string{"-t", "module", "-j"}, modules[start:end]...)
		output, err := runTool("ansible-doc", args...)
		if err != nil {
			return nil, err
		}
		parsed, err := ParseAnsibleDoc(output)
		if err != nil {
			return nil, err
		}
		entries = append(entries, parsed...)
		if progress != nil {
			progress(end)
		}
	}
	return entries, nil
}

func runTool(name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, fmt.Errorf("%s is not installed (or not on PATH)", name)
	}
	command := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), firstLines(stderr.String(), 6), err)
	}
	return stdout.Bytes(), nil
}

func firstLines(text string, count int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > count {
		lines = append(lines[:count], "...")
	}
	return strings.Join(lines, "\n")
}

type ansibleOption struct {
	Description any                      `json:"description"`
	Type        string                   `json:"type"`
	Elements    string                   `json:"elements"`
	Required    bool                     `json:"required"`
	Default     any                      `json:"default"`
	Choices     any                      `json:"choices"`
	Aliases     []string                 `json:"aliases"`
	Suboptions  map[string]ansibleOption `json:"suboptions"`
}

// ParseAnsibleDoc reads ansible-doc -j output (a map from module name to its doc,
// examples and return values).
func ParseAnsibleDoc(data []byte) ([]Entry, error) {
	var docs map[string]struct {
		Doc struct {
			ShortDescription string                   `json:"short_description"`
			Options          map[string]ansibleOption `json:"options"`
		} `json:"doc"`
		Examples string `json:"examples"`
	}
	if err := json.Unmarshal(data, &docs); err != nil {
		return nil, fmt.Errorf("reading ansible-doc JSON: %w", err)
	}
	var entries []Entry
	for name, doc := range docs {
		entries = append(entries, Entry{
			Name:    name,
			Type:    "module",
			Summary: cleanAnsibleMarkup(doc.Doc.ShortDescription),
			Options: ansibleOptions(doc.Doc.Options),
			Example: firstExample(doc.Examples),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func ansibleOptions(options map[string]ansibleOption) []Option {
	var out []Option
	for name, option := range options {
		out = append(out, Option{
			Name:     name,
			Aliases:  option.Aliases,
			Type:     option.Type,
			Elements: option.Elements,
			Required: option.Required,
			Choices:  choiceStrings(option.Choices),
			Default:  valueString(option.Default),
			Desc:     firstSentence(option.Description),
			Sub:      ansibleOptions(option.Suboptions),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// choiceStrings accepts a list of choices or (newer ansible-doc) a map from choice to
// its description.
func choiceStrings(choices any) []string {
	var out []string
	switch value := choices.(type) {
	case []any:
		for _, choice := range value {
			out = append(out, valueString(choice))
		}
	case map[string]any:
		for choice := range value {
			out = append(out, choice)
		}
		sort.Strings(out)
	}
	return out
}

func valueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		data, _ := json.Marshal(typed)
		return string(data)
	}
}

var (
	ansibleMarkupRE = regexp.MustCompile(`\b[CIBVOMEPRU]\(([^()]*)\)`)
	ansibleLinkRE   = regexp.MustCompile(`\bL\(([^,()]*),[^()]*\)`)
	sentenceEndRE   = regexp.MustCompile(`[.!?](\s|$)`)
)

// cleanAnsibleMarkup turns C(code), I(option), L(text,url) and the like into plain text.
func cleanAnsibleMarkup(text string) string {
	text = ansibleLinkRE.ReplaceAllString(text, "$1")
	return strings.TrimSpace(ansibleMarkupRE.ReplaceAllString(text, "$1"))
}

// firstSentence is the first sentence of a description (a string or a list of
// paragraphs), at most 160 characters.
func firstSentence(description any) string {
	text := ""
	switch value := description.(type) {
	case string:
		text = value
	case []any:
		if len(value) > 0 {
			text = valueString(value[0])
		}
	}
	text = strings.Join(strings.Fields(cleanAnsibleMarkup(text)), " ")
	if location := sentenceEndRE.FindStringIndex(text); location != nil {
		text = text[:location[0]+1]
	}
	if runes := []rune(text); len(runes) > 160 {
		text = string(runes[:157]) + "..."
	}
	return text
}

// firstExample is the first task of a module's EXAMPLES, at most 30 lines.
func firstExample(examples string) string {
	lines := strings.Split(strings.Trim(examples, "\n"), "\n")
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(line, "- ") && len(out) > 0 {
			break
		}
		if len(out) == 0 && (strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#")) {
			continue
		}
		out = append(out, line)
		if len(out) == 30 {
			break
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n ")
}

// CommonOptions finds the options that nearly every module of a large set accepts (in
// Azure, AWS and similar collections: authentication and connection settings). They are
// shown once per prompt instead of in every module, and still pass the checks.
func CommonOptions(entries []Entry) []Option {
	if len(entries) < 10 {
		return nil
	}
	counts := map[string]int{}
	first := map[string]Option{}
	for _, entry := range entries {
		for _, option := range entry.Options {
			counts[option.Name]++
			if _, ok := first[option.Name]; !ok {
				first[option.Name] = option
			}
		}
	}
	var common []Option
	for name, count := range counts {
		if count*10 >= len(entries)*9 && !first[name].Required {
			common = append(common, first[name])
		}
	}
	sort.Slice(common, func(i, j int) bool { return common[i].Name < common[j].Name })
	return common
}
