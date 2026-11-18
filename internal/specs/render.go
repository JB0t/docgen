package specs

import (
	"fmt"
	"strings"
)

// Render writes an entry for a prompt. Compact drops descriptions and examples, for
// when the full form does not fit. Options in skip (a set's common options) are left out.
func Render(set *Set, entry *Entry, compact bool) string {
	var out strings.Builder
	skip := map[string]bool{}
	for _, option := range set.Common {
		skip[option.Name] = true
	}
	switch entry.Type {
	case "module":
		fmt.Fprintf(&out, "### Ansible module %s\n", entry.Name)
		writeSummary(&out, entry.Summary)
		if len(entry.Options) > 0 {
			out.WriteString("Options (* = required):\n")
			writeOptions(&out, entry.Options, skip, 0, compact, false)
		}
		if len(set.Common) > 0 {
			fmt.Fprintf(&out, "(Also accepts the options common to %s, listed separately.)\n", set.Name)
		}
		if entry.Example != "" && !compact {
			out.WriteString("Example from its documentation:\n```yaml\n" + entry.Example + "\n```\n")
		}
	case "resource", "data":
		fmt.Fprintf(&out, "### Terraform %s %q (%s)\n", entry.Type, entry.Name, set.Name)
		writeSummary(&out, entry.Summary)
		out.WriteString("Arguments and blocks (* = required; computed-only attributes are read-only and not listed):\n")
		writeOptions(&out, entry.Options, nil, 0, compact, true)
	case "action":
		fmt.Fprintf(&out, "### GitHub Action uses: %s\n", entry.Name)
		writeSummary(&out, entry.Summary)
		if len(entry.Options) > 0 {
			out.WriteString("Inputs (under with:; * = required):\n")
			writeOptions(&out, entry.Options, nil, 0, compact, false)
		} else {
			out.WriteString("No inputs.\n")
		}
		if len(entry.Outputs) > 0 {
			var names []string
			for _, output := range entry.Outputs {
				names = append(names, output.Name)
			}
			out.WriteString("Outputs (steps.<id>.outputs.<name>): " + strings.Join(names, ", ") + "\n")
		}
	default:
		fmt.Fprintf(&out, "### %s\n", entry.Name)
		text := entry.Text
		if compact && len([]rune(text)) > 800 {
			text = string([]rune(text)[:800]) + "\n..."
		}
		out.WriteString(text + "\n")
	}
	return out.String()
}

// RenderCommon lists a set's common options once.
func RenderCommon(set *Set) string {
	if len(set.Common) == 0 {
		return ""
	}
	var names []string
	for _, option := range set.Common {
		names = append(names, option.Name)
	}
	return fmt.Sprintf("### Options every %s module accepts\nUsually authentication and connection settings; leave them out unless the request needs them (credentials come from the environment): %s\n",
		set.Name, strings.Join(names, ", "))
}

func writeSummary(out *strings.Builder, summary string) {
	if summary != "" {
		out.WriteString(summary + "\n")
	}
}

func writeOptions(out *strings.Builder, options []Option, skip map[string]bool, depth int, compact, terraform bool) {
	indent := strings.Repeat("    ", depth)
	for _, option := range options {
		if skip[option.Name] || option.Computed {
			continue
		}
		line := indent + "- " + option.Name
		if option.Required {
			line += "*"
		}
		var details []string
		if option.Block {
			details = append(details, "block, "+option.Type)
		} else if option.Type != "" {
			kind := option.Type
			if option.Elements != "" {
				kind += " of " + option.Elements
			}
			details = append(details, kind)
		}
		if len(option.Choices) > 0 {
			details = append(details, "choices: "+strings.Join(option.Choices, "|"))
		}
		if option.Default != "" && len(option.Default) <= 40 {
			details = append(details, "default: "+option.Default)
		}
		if len(option.Aliases) > 0 && !compact {
			details = append(details, "aliases: "+strings.Join(option.Aliases, ", "))
		}
		if len(details) > 0 {
			line += " (" + strings.Join(details, "; ") + ")"
		}
		if option.Desc != "" && !compact {
			line += ": " + option.Desc
		}
		out.WriteString(line + "\n")
		if len(option.Sub) > 0 && depth < 2 {
			writeOptions(out, option.Sub, nil, depth+1, compact, terraform)
		}
	}
}
