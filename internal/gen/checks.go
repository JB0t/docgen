package gen

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"gopkg.in/yaml.v3"

	"github.com/JB0t/docgen/internal/specs"
)

// Problem is one finding of a check.
type Problem struct {
	Check   string // which check found it
	File    string // root-relative, when known
	Line    int
	Message string
	// Advice findings (linters' style rules) are reported but neither sent back to the
	// model nor block writing the files.
	Advice bool
}

func (p Problem) String() string {
	location := ""
	if p.File != "" {
		location = p.File
		if p.Line > 0 {
			location += fmt.Sprintf(":%d", p.Line)
		}
		location += ": "
	}
	return fmt.Sprintf("[%s] %s%s", p.Check, location, p.Message)
}

// CheckContext is what a check sees.
type CheckContext struct {
	Stage    *Stage
	Files    []string          // generated or changed files, root-relative, sorted
	Contents map[string]string // their content
	Sets     []*specs.Set      // the spec store
	Inputs   []Input           // inputs the context files expect (playbook variables, ...)
}

// Check is one validation. Tool names a program it needs; without it the check is
// skipped with a note. A formatter rewrites the staged files, which are read back.
type Check struct {
	Name      string
	Tool      string
	Advice    bool
	Formatter bool
	Run       func(ctx *CheckContext) []Problem
}

// Report is the result of running the checks.
type Report struct {
	Problems []Problem // errors: sent back to the model, block writing
	Advice   []Problem
	Skipped  []string // checks whose tool is missing, with why
	Ran      []string
}

// RunChecks runs a kind's checks plus extra commands on the staged files; formatters
// run first and may rewrite ctx.Contents.
func RunChecks(checks []Check, extra []string, ctx *CheckContext) Report {
	var report Report
	ordered := append([]Check(nil), checks...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Formatter && !ordered[j].Formatter })
	for _, command := range extra {
		ordered = append(ordered, commandCheck(command))
	}
	for _, check := range ordered {
		if check.Tool != "" {
			if _, err := exec.LookPath(check.Tool); err != nil {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s (%s is not installed)", check.Name, check.Tool))
				continue
			}
		}
		report.Ran = append(report.Ran, check.Name)
		for _, problem := range check.Run(ctx) {
			if problem.Check == "" {
				problem.Check = check.Name
			}
			if check.Advice || problem.Advice {
				problem.Advice = true
				report.Advice = append(report.Advice, problem)
			} else {
				report.Problems = append(report.Problems, problem)
			}
		}
		if check.Formatter {
			for _, rel := range ctx.Files {
				if content, err := ctx.Stage.Read(rel); err == nil {
					ctx.Contents[rel] = content
				}
			}
		}
	}
	return report
}

// toolTimeout bounds every external check.
const toolTimeout = 3 * time.Minute

// commandCheck runs a configured command (gen.validate) through sh in the stage root.
func commandCheck(command string) Check {
	return Check{Name: command, Tool: "sh", Run: func(ctx *CheckContext) []Problem {
		quoted := make([]string, len(ctx.Files))
		for index, file := range ctx.Files {
			quoted[index] = shellQuote(file)
		}
		line := strings.ReplaceAll(command, "{files}", strings.Join(quoted, " "))
		line = strings.ReplaceAll(line, "{dir}", shellQuote(commonDir(ctx.Files)))
		output, ok, err := ctx.Stage.Run(".", toolTimeout, "sh", "-c", line)
		if err != nil {
			return []Problem{{Message: err.Error()}}
		}
		if !ok {
			return []Problem{{Message: "failed:\n" + trimOutput(output)}}
		}
		return nil
	}}
}

func shellQuote(text string) string { return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'" }

// trimOutput keeps tool output short enough for a prompt.
func trimOutput(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) > 40 {
		lines = append(lines[:40], fmt.Sprintf("... (%d more lines)", len(lines)-40))
	}
	return strings.Join(lines, "\n")
}

// filesWith returns the changed files with one of the extensions.
func filesWith(ctx *CheckContext, extensions ...string) []string {
	var out []string
	for _, file := range ctx.Files {
		for _, extension := range extensions {
			if strings.HasSuffix(file, extension) {
				out = append(out, file)
				break
			}
		}
	}
	return out
}

// isHelmTemplate reports files that are Go templates rather than plain YAML.
func isHelmTemplate(file string) bool {
	return strings.Contains("/"+file, "/templates/") || strings.HasSuffix(file, ".tpl")
}

// syntaxCheck parses YAML, JSON and HCL files.
var syntaxCheck = Check{Name: "syntax", Run: func(ctx *CheckContext) []Problem {
	var problems []Problem
	for _, file := range ctx.Files {
		content := ctx.Contents[file]
		switch path.Ext(file) {
		case ".yml", ".yaml":
			if isHelmTemplate(file) {
				continue
			}
			decoder := yaml.NewDecoder(strings.NewReader(content))
			for {
				var node yaml.Node
				err := decoder.Decode(&node)
				if err == nil {
					continue
				}
				if err.Error() != "EOF" {
					problems = append(problems, Problem{File: file, Message: "invalid YAML: " + err.Error()})
				}
				break
			}
		case ".json":
			var value any
			if err := json.Unmarshal([]byte(content), &value); err != nil {
				problems = append(problems, Problem{File: file, Message: "invalid JSON: " + err.Error()})
			}
		case ".tf", ".hcl":
			if _, diagnostics := hclsyntax.ParseConfig([]byte(content), file, hcl.InitialPos); diagnostics.HasErrors() {
				for _, diagnostic := range diagnostics {
					line := 0
					if diagnostic.Subject != nil {
						line = diagnostic.Subject.Start.Line
					}
					problems = append(problems, Problem{File: file, Line: line, Message: diagnostic.Summary + ": " + diagnostic.Detail})
				}
			}
		}
	}
	return problems
}}
