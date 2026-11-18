package extract

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlExtractor understands the YAML that carries documentation-worthy names:
// Helm charts (Chart.yaml, values.yaml keys with their comments), Ansible playbooks,
// roles and tasks, GitHub workflows (jobs, inputs), Compose services and Kubernetes
// manifests (kind/name).
type yamlExtractor struct{ flavour string }

func (y yamlExtractor) Language() string { return y.flavour }

func (y yamlExtractor) Extract(path string, source []byte) Result {
	result := Result{EnvVars: EnvVars(y.flavour, path, source)}
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			break // end of stream, or templated YAML (Helm templates) that does not parse
		}
		if len(document.Content) == 0 {
			continue
		}
		root := document.Content[0]
		base := strings.ToLower(filepath.Base(path))
		slashed := filepath.ToSlash(path)
		switch {
		case base == "chart.yaml":
			result.Symbols = append(result.Symbols, y.symbol(path, root, mapString(root, "name"), "chart", mapString(root, "description"), "version "+mapString(root, "version")))
		case base == "values.yaml":
			y.values(path, root, "", &result.Symbols)
		case strings.Contains(slashed, ".github/workflows/"):
			y.workflow(path, root, &result.Symbols)
		case y.flavour == "ansible":
			y.ansible(path, root, &result.Symbols)
		case mapString(root, "kind") != "" && mapString(root, "apiVersion") != "":
			metadata := mapValue(root, "metadata")
			result.Symbols = append(result.Symbols, y.symbol(path, root, mapString(root, "kind")+"/"+mapString(metadata, "name"), "manifest", "", mapString(root, "apiVersion")))
		case mapValue(root, "services") != nil && (strings.Contains(base, "compose")):
			for _, pair := range pairs(mapValue(root, "services")) {
				result.Symbols = append(result.Symbols, y.symbol(path, pair[0], pair[0].Value, "service", comment(pair[0]), ""))
			}
		}
	}
	return result
}

func (y yamlExtractor) symbol(path string, node *yaml.Node, name, kind, doc, signature string) Symbol {
	return Symbol{Name: name, Kind: kind, Language: y.flavour, File: path, Line: node.Line, Doc: doc,
		Signature: strings.TrimSpace(signature), Exported: true}
}

// values records every values.yaml key as a dotted path, with its comment as doc.
func (y yamlExtractor) values(path string, node *yaml.Node, prefix string, symbols *[]Symbol) {
	for _, pair := range pairs(node) {
		name := pair[0].Value
		if prefix != "" {
			name = prefix + "." + name
		}
		signature := ""
		if pair[1].Kind == yaml.ScalarNode {
			signature = "default: " + pair[1].Value
		}
		*symbols = append(*symbols, y.symbol(path, pair[0], name, "value", comment(pair[0]), signature))
		if pair[1].Kind == yaml.MappingNode && strings.Count(name, ".") < 4 {
			y.values(path, pair[1], name, symbols)
		}
	}
}

func (y yamlExtractor) workflow(path string, root *yaml.Node, symbols *[]Symbol) {
	name := mapString(root, "name")
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	*symbols = append(*symbols, y.symbol(path, root, name, "workflow", comment(root), ""))
	on := mapValue(root, "on")
	for _, trigger := range []string{"workflow_dispatch", "workflow_call"} {
		for _, input := range pairs(mapValue(mapValue(on, trigger), "inputs")) {
			*symbols = append(*symbols, y.symbol(path, input[0], input[0].Value, "input", mapString(input[1], "description"), "required: "+mapString(input[1], "required")))
		}
	}
	for _, job := range pairs(mapValue(root, "jobs")) {
		*symbols = append(*symbols, y.symbol(path, job[0], job[0].Value, "job", mapString(job[1], "name"), ""))
	}
}

func (y yamlExtractor) ansible(path string, root *yaml.Node, symbols *[]Symbol) {
	slashed := filepath.ToSlash(path)
	if index := strings.Index(slashed, "roles/"); index >= 0 {
		role := strings.SplitN(slashed[index+len("roles/"):], "/", 2)[0]
		if strings.Contains(slashed, "/defaults/") || strings.Contains(slashed, "/vars/") {
			for _, pair := range pairs(root) {
				*symbols = append(*symbols, Symbol{Name: pair[0].Value, Kind: "variable", Language: "ansible", File: path,
					Line: pair[0].Line, Parent: role, Doc: comment(pair[0]), Exported: true})
			}
			return
		}
	}
	if root.Kind != yaml.SequenceNode {
		return
	}
	for _, item := range root.Content {
		if hosts := mapString(item, "hosts"); hosts != "" {
			*symbols = append(*symbols, y.symbol(path, item, mapString(item, "name"), "play", comment(item), "hosts: "+hosts))
			// A play may have no tasks: (roles only, or pre_tasks/post_tasks only).
			for _, key := range []string{"pre_tasks", "tasks", "post_tasks"} {
				list := mapValue(item, key)
				if list == nil || list.Kind != yaml.SequenceNode {
					continue
				}
				for _, task := range list.Content {
					if name := mapString(task, "name"); name != "" {
						*symbols = append(*symbols, y.symbol(path, task, name, "task", "", ""))
					}
				}
			}
			continue
		}
		if name := mapString(item, "name"); name != "" {
			*symbols = append(*symbols, y.symbol(path, item, name, "task", comment(item), ""))
		}
	}
}

func pairs(node *yaml.Node) [][2]*yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	var out [][2]*yaml.Node
	for index := 0; index+1 < len(node.Content); index += 2 {
		out = append(out, [2]*yaml.Node{node.Content[index], node.Content[index+1]})
	}
	return out
}

func mapValue(node *yaml.Node, key string) *yaml.Node {
	for _, pair := range pairs(node) {
		if pair[0].Value == key {
			return pair[1]
		}
	}
	// GitHub's `on:` key is parsed as boolean true by some YAML 1.1 readers.
	if key == "on" {
		return mapValue(node, "true")
	}
	return nil
}

func mapString(node *yaml.Node, key string) string {
	value := mapValue(node, key)
	if value == nil || value.Kind != yaml.ScalarNode {
		return ""
	}
	return value.Value
}

func comment(node *yaml.Node) string {
	text := strings.TrimSpace(node.HeadComment + "\n" + node.LineComment)
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#")))
	}
	return strings.TrimSpace(fmt.Sprint(strings.Join(lines, "\n")))
}
