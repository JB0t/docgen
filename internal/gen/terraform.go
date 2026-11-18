package gen

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/JB0t/docgen/internal/specs"
)

var (
	tfResourceMeta = toSet("count for_each provider depends_on lifecycle provisioner connection")
)

// terraformSpecCheck checks resource and data blocks against the stored provider
// schemas: the type exists, every argument and nested block exists and is writable, and
// required ones are present. It works without terraform init.
var terraformSpecCheck = Check{Name: "terraform specs", Run: func(ctx *CheckContext) []Problem {
	types := map[string]map[string]*specs.Entry{"resource": {}, "data": {}}
	prefixes := map[string]bool{}
	names := map[string][]string{}
	for _, set := range ctx.Sets {
		if set.Kind != specs.KindTerraform {
			continue
		}
		for index := range set.Entries {
			entry := &set.Entries[index]
			types[entry.Type][entry.Name] = entry
			names[entry.Type] = append(names[entry.Type], entry.Name)
			prefix, _, _ := strings.Cut(entry.Name, "_")
			prefixes[prefix] = true
		}
	}
	if len(prefixes) == 0 {
		return nil
	}
	var problems []Problem
	for _, file := range filesWith(ctx, ".tf") {
		parsed, diagnostics := hclsyntax.ParseConfig([]byte(ctx.Contents[file]), file, hcl.InitialPos)
		if diagnostics.HasErrors() {
			continue // the syntax check reports it
		}
		body, ok := parsed.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, block := range body.Blocks {
			if (block.Type != "resource" && block.Type != "data") || len(block.Labels) < 1 {
				continue
			}
			resourceType := block.Labels[0]
			entry := types[block.Type][resourceType]
			if entry == nil {
				prefix, _, _ := strings.Cut(resourceType, "_")
				if prefixes[prefix] {
					message := fmt.Sprintf("there is no %s type %s in the stored provider schema", block.Type, resourceType)
					if closest := specs.Closest(resourceType, names[block.Type]); closest != "" {
						message += fmt.Sprintf(" (did you mean %s?)", closest)
					}
					problems = append(problems, Problem{File: file, Line: block.TypeRange.Start.Line, Message: message})
				}
				continue
			}
			for _, message := range checkTFBody(resourceType, block.Body, entry.Options, true) {
				problems = append(problems, Problem{File: file, Line: message.line, Message: message.text})
			}
		}
	}
	return problems
}}

func checkTFBody(owner string, body *hclsyntax.Body, options []specs.Option, topLevel bool) []lineMessage {
	byName := map[string]*specs.Option{}
	var names []string
	for index := range options {
		byName[options[index].Name] = &options[index]
		if !options[index].Computed {
			names = append(names, options[index].Name)
		}
	}
	var messages []lineMessage
	given := map[string]bool{}
	attributeNames := make([]string, 0, len(body.Attributes))
	for name := range body.Attributes {
		attributeNames = append(attributeNames, name)
	}
	sort.Strings(attributeNames)
	for _, name := range attributeNames {
		attribute := body.Attributes[name]
		line := attribute.SrcRange.Start.Line
		if topLevel && tfResourceMeta[name] {
			continue
		}
		option := byName[name]
		switch {
		case option == nil:
			text := fmt.Sprintf("%s has no argument %q", owner, name)
			if closest := specs.Closest(name, names); closest != "" {
				text += fmt.Sprintf(" (did you mean %q?)", closest)
			}
			messages = append(messages, lineMessage{line, text})
		case option.Computed:
			messages = append(messages, lineMessage{line, fmt.Sprintf("%s.%s is computed by the provider and cannot be set", owner, name)})
		case option.Block:
			messages = append(messages, lineMessage{line, fmt.Sprintf("%s.%s is a nested block: write %s { ... } instead of %s = ...", owner, name, name, name)})
		}
		given[name] = true
	}
	for _, block := range body.Blocks {
		name, nested := block.Type, block.Body
		line := block.TypeRange.Start.Line
		if topLevel && tfResourceMeta[name] {
			continue
		}
		if name == "dynamic" && len(block.Labels) == 1 {
			name = block.Labels[0]
			nested = nil
			for _, inner := range block.Body.Blocks {
				if inner.Type == "content" {
					nested = inner.Body
				}
			}
		}
		option := byName[name]
		switch {
		case option == nil:
			text := fmt.Sprintf("%s has no block %q", owner, name)
			if closest := specs.Closest(name, names); closest != "" {
				text += fmt.Sprintf(" (did you mean %q?)", closest)
			}
			messages = append(messages, lineMessage{line, text})
		case !option.Block:
			messages = append(messages, lineMessage{line, fmt.Sprintf("%s.%s is an argument: write %s = ... instead of a block", owner, name, name)})
		case nested != nil:
			messages = append(messages, checkTFBody(owner+"."+name, nested, option.Sub, false)...)
		}
		given[name] = true
	}
	for _, option := range options {
		if option.Required && !given[option.Name] {
			kind := "argument"
			if option.Block {
				kind = "block"
			}
			messages = append(messages, lineMessage{body.SrcRange.Start.Line, fmt.Sprintf("%s is missing the required %s %q", owner, kind, option.Name)})
		}
	}
	return messages
}

// tfDirs are the directories holding generated .tf files.
func tfDirs(ctx *CheckContext) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, file := range filesWith(ctx, ".tf") {
		if dir := path.Dir(file); !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	return dirs
}

// terraformFmtCheck formats the generated files (terraform fmt), so they read as
// Terraform's own style; parse errors are left to the syntax check.
var terraformFmtCheck = Check{Name: "terraform fmt", Tool: "terraform", Formatter: true, Run: func(ctx *CheckContext) []Problem {
	for _, file := range filesWith(ctx, ".tf", ".tfvars") {
		_, _, _ = ctx.Stage.Run(path.Dir(file), toolTimeout, "terraform", "fmt", "-no-color", path.Base(file))
	}
	return nil
}}

// terraformValidateCheck runs terraform validate where the directory has been
// initialised (.terraform exists); elsewhere it says how to enable it.
var terraformValidateCheck = Check{Name: "terraform validate", Tool: "terraform", Run: func(ctx *CheckContext) []Problem {
	var problems []Problem
	for _, dir := range tfDirs(ctx) {
		if _, err := os.Stat(ctx.Stage.Path(path.Join(dir, ".terraform"))); err != nil {
			problems = append(problems, Problem{File: dir, Advice: true, Message: "not checked with terraform validate: run terraform init there first (the spec check still ran)"})
			continue
		}
		output, ok, err := ctx.Stage.Run(dir, toolTimeout, "terraform", "validate", "-no-color")
		if err != nil {
			problems = append(problems, Problem{File: dir, Message: err.Error()})
		} else if !ok {
			problems = append(problems, Problem{File: dir, Message: "terraform validate failed:\n" + trimOutput(output)})
		}
	}
	return problems
}}

// TerraformInputs lists the variable blocks of .tf files; those without a default are
// required.
func TerraformInputs(files map[string]string) []Input {
	var inputs []Input
	for _, file := range sortedFiles(files) {
		if !strings.HasSuffix(file, ".tf") {
			continue
		}
		parsed, diagnostics := hclsyntax.ParseConfig([]byte(files[file]), file, hcl.InitialPos)
		if diagnostics.HasErrors() {
			continue
		}
		body, ok := parsed.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, block := range body.Blocks {
			if block.Type == "variable" && len(block.Labels) == 1 {
				_, hasDefault := block.Body.Attributes["default"]
				inputs = append(inputs, Input{Name: block.Labels[0], Optional: hasDefault, File: file})
			}
		}
	}
	return inputs
}
