package gen

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/JB0t/docgen/internal/specs"
)

var (
	// untrustedInRunRE finds values a person or an event controls, expanded straight into a script.
	untrustedInRunRE = regexp.MustCompile(`\$\{\{\s*(?:inputs\.|github\.event\.|github\.head_ref)[^}]*\}\}`)
	inputRefRE       = regexp.MustCompile(`\$\{\{[^}]*?\b(?:github\.event\.)?inputs\.([A-Za-z_][A-Za-z0-9_-]*)`)
	gitlabRefRE      = regexp.MustCompile(`\$\[\[\s*inputs\.([A-Za-z_][A-Za-z0-9_-]*)`)
)

// githubWorkflows returns the generated files that are workflows (have jobs:), parsed.
func githubWorkflows(ctx *CheckContext) map[string]*yaml.Node {
	workflows := map[string]*yaml.Node{}
	for _, file := range filesWith(ctx, ".yml", ".yaml") {
		documents := yamlDocuments(ctx.Contents[file])
		if len(documents) == 1 && get(documents[0], "jobs") != nil {
			workflows[file] = documents[0]
		}
	}
	return workflows
}

// githubStructureCheck checks the shape of workflows: on and jobs, a runner or
// reusable workflow per job, uses or run per step, and that every ${{ inputs.X }} is
// declared under workflow_dispatch or workflow_call.
var githubStructureCheck = Check{Name: "workflow structure", Run: func(ctx *CheckContext) []Problem {
	var problems []Problem
	workflows := githubWorkflows(ctx)
	for _, file := range sortedFiles(workflows) {
		workflow := workflows[file]
		on := get(workflow, "on")
		if on == nil {
			problems = append(problems, Problem{File: file, Message: "the workflow has no on: trigger"})
		}
		declared := map[string]bool{}
		for _, trigger := range []string{"workflow_dispatch", "workflow_call"} {
			for _, pair := range mapping(get(get(on, trigger), "inputs")) {
				declared[pair[0].Value] = true
			}
		}
		for _, match := range inputRefRE.FindAllStringSubmatch(ctx.Contents[file], -1) {
			if !declared[match[1]] {
				problems = append(problems, Problem{File: file, Message: fmt.Sprintf("inputs.%s is used but not declared under on.workflow_dispatch.inputs (or workflow_call)", match[1])})
				declared[match[1]] = true // once per name
			}
		}
		problems = append(problems, oidcPermissionProblems(file, workflow)...)
		problems = append(problems, checkoutPermissionProblems(file, workflow)...)
		for _, job := range mapping(get(workflow, "jobs")) {
			if steps := get(job[1], "steps"); steps != nil {
				for _, step := range steps.Content {
					if run := get(step, "run"); run != nil && untrustedInRunRE.MatchString(run.Value) {
						problems = append(problems, Problem{File: file, Line: run.Line, Message: fmt.Sprintf("job %s: a run: script expands %s directly, which lets a value inject shell code; set it in the step's env: (NAME: ${{ inputs.x }}) and use \"$NAME\" in the script", job[0].Value, untrustedInRunRE.FindString(run.Value))})
					}
				}
			}
			if get(job[1], "runs-on") == nil && get(job[1], "uses") == nil {
				problems = append(problems, Problem{File: file, Line: job[0].Line, Message: fmt.Sprintf("job %s has neither runs-on nor uses", job[0].Value)})
			}
			if steps := get(job[1], "steps"); steps != nil {
				for number, step := range steps.Content {
					if get(step, "uses") == nil && get(step, "run") == nil {
						problems = append(problems, Problem{File: file, Line: step.Line, Message: fmt.Sprintf("step %d of job %s has neither uses nor run", number+1, job[0].Value)})
					}
				}
			}
		}
	}
	return problems
}}

func sortedFiles[T any](files map[string]T) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// actionInputsCheck checks the with: of every step whose action has a spec.
var actionInputsCheck = Check{Name: "action inputs", Run: func(ctx *CheckContext) []Problem {
	byRepo := map[string][]*specs.Entry{}
	for _, set := range ctx.Sets {
		if set.Kind != specs.KindAction {
			continue
		}
		for index := range set.Entries {
			entry := &set.Entries[index]
			byRepo[specs.ActionRepo(entry.Name)] = append(byRepo[specs.ActionRepo(entry.Name)], entry)
		}
	}
	if len(byRepo) == 0 {
		return nil
	}
	var problems []Problem
	workflows := githubWorkflows(ctx)
	for _, file := range sortedFiles(workflows) {
		for _, job := range mapping(get(workflows[file], "jobs")) {
			steps := get(job[1], "steps")
			if steps == nil {
				continue
			}
			for _, step := range steps.Content {
				uses := get(step, "uses")
				if uses == nil || !strings.Contains(uses.Value, "@") || strings.HasPrefix(uses.Value, "./") {
					continue
				}
				candidates := byRepo[specs.ActionRepo(uses.Value)]
				if len(candidates) == 0 {
					continue
				}
				entry := candidates[0]
				for _, candidate := range candidates {
					if strings.EqualFold(candidate.Name, uses.Value) {
						entry = candidate
					}
				}
				if !strings.EqualFold(entry.Name, uses.Value) {
					problems = append(problems, Problem{File: file, Line: uses.Line, Advice: true, Message: fmt.Sprintf("the stored spec is for %s; this step uses %s (inputs checked against the spec)", entry.Name, uses.Value)})
				}
				with := get(step, "with")
				if with == nil {
					with = &yaml.Node{Kind: yaml.MappingNode, Line: uses.Line}
				}
				for _, message := range checkOptions(entry.Name, with, entry.Options, true) {
					problems = append(problems, Problem{File: file, Line: message.line, Message: message.text})
				}
			}
		}
	}
	return problems
}}

// actionlintCheck runs actionlint; shellcheck findings it relays are advice.
var actionlintCheck = Check{Name: "actionlint", Tool: "actionlint", Run: func(ctx *CheckContext) []Problem {
	var files []string
	for _, file := range sortedFiles(githubWorkflows(ctx)) {
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil
	}
	output, ok, err := ctx.Stage.Run(".", toolTimeout, "actionlint", append([]string{"-no-color", "-oneline"}, files...)...)
	if err != nil {
		return []Problem{{Message: err.Error()}}
	}
	if ok {
		return nil
	}
	var problems []Problem
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		problems = append(problems, Problem{Message: line, Advice: strings.Contains(line, "[shellcheck]")})
	}
	return problems
}}

var gitlabReserved = toSet("default include stages variables workflow image services cache before_script after_script spec")

// gitlabStructureCheck checks a pipeline: jobs with a script (or trigger, extends or
// run), stages that are declared, needs and extends that name jobs of the file, and
// $[[ inputs.X ]] declared in the spec: header.
var gitlabStructureCheck = Check{Name: "pipeline structure", Run: func(ctx *CheckContext) []Problem {
	var problems []Problem
	for _, file := range filesWith(ctx, ".yml", ".yaml") {
		documents := yamlDocuments(ctx.Contents[file])
		if len(documents) == 0 {
			continue
		}
		declaredInputs := map[string]bool{}
		pipeline := documents[len(documents)-1]
		if len(documents) > 1 {
			for _, pair := range mapping(get(get(documents[0], "spec"), "inputs")) {
				declaredInputs[pair[0].Value] = true
			}
		}
		for _, match := range gitlabRefRE.FindAllStringSubmatch(ctx.Contents[file], -1) {
			if !declaredInputs[match[1]] {
				problems = append(problems, Problem{File: file, Message: fmt.Sprintf("$[[ inputs.%s ]] is used but not declared in the spec: inputs header (a first YAML document ending with ---)", match[1])})
				declaredInputs[match[1]] = true
			}
		}
		stages := map[string]bool{".pre": true, ".post": true}
		if declared := get(pipeline, "stages"); declared != nil {
			for _, stage := range declared.Content {
				stages[stage.Value] = true
			}
		} else {
			stages["build"], stages["test"], stages["deploy"] = true, true, true
		}
		jobs := map[string]*yaml.Node{}
		for _, pair := range mapping(pipeline) {
			if !gitlabReserved[pair[0].Value] {
				jobs[pair[0].Value] = pair[1]
			}
		}
		includes := get(pipeline, "include") != nil
		for _, name := range sortedFiles(jobs) {
			job := jobs[name]
			if job.Kind != yaml.MappingNode {
				problems = append(problems, Problem{File: file, Line: job.Line, Message: fmt.Sprintf("%s is not a job (a mapping) or a known top-level keyword", name)})
				continue
			}
			hidden := strings.HasPrefix(name, ".")
			if !hidden && get(job, "script") == nil && get(job, "trigger") == nil && get(job, "extends") == nil && get(job, "run") == nil {
				problems = append(problems, Problem{File: file, Line: job.Line, Message: fmt.Sprintf("job %s has no script (or trigger, extends, run)", name)})
			}
			if stage := get(job, "stage"); stage != nil && !strings.Contains(stage.Value, "$") && !stages[stage.Value] {
				problems = append(problems, Problem{File: file, Line: stage.Line, Message: fmt.Sprintf("job %s uses stage %q, which stages: does not declare", name, stage.Value)})
			}
			if includes {
				continue // needs and extends may name jobs from included files
			}
			for _, key := range []string{"needs", "extends", "dependencies"} {
				value := get(job, key)
				if value == nil {
					continue
				}
				var names []*yaml.Node
				switch value.Kind {
				case yaml.ScalarNode:
					names = append(names, value)
				case yaml.SequenceNode:
					for _, item := range value.Content {
						if item.Kind == yaml.ScalarNode {
							names = append(names, item)
						} else if job := get(item, "job"); job != nil {
							names = append(names, job)
						}
					}
				}
				for _, other := range names {
					if _, ok := jobs[other.Value]; !ok {
						problems = append(problems, Problem{File: file, Line: other.Line, Message: fmt.Sprintf("job %s: %s names %q, which is not a job in this file", name, key, other.Value)})
					}
				}
			}
		}
	}
	return problems
}}

// contextInputsCheck makes sure a generated workflow or pipeline passes every value the
// context files need (the variables a playbook uses without defining them).
var contextInputsCheck = Check{Name: "context inputs", Run: func(ctx *CheckContext) []Problem {
	var problems []Problem
	for _, input := range ctx.Inputs {
		if input.Optional {
			continue
		}
		word := regexp.MustCompile(`\b` + regexp.QuoteMeta(input.Name) + `\b`)
		found := false
		for _, file := range ctx.Files {
			if word.MatchString(ctx.Contents[file]) {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, Problem{Message: fmt.Sprintf("%s needs %s, but no generated file passes it: add it as an input and pass it on (to a playbook with -e or --extra-vars)", input.File, input.Name)})
			continue
		}
		if input.Structured {
			keyValue := regexp.MustCompile(`(?:-e|--extra-vars)[ =]+["']?` + regexp.QuoteMeta(input.Name) + `=`)
			for _, file := range ctx.Files {
				if keyValue.MatchString(ctx.Contents[file]) {
					problems = append(problems, Problem{File: file, Message: fmt.Sprintf("passes %s as -e %s=..., which Ansible reads as a string, but %s loops over it: pass it as JSON, for example -e \"{\\\"%s\\\": $VALUE}\" with VALUE holding the JSON", input.Name, input.Name, input.File, input.Name)})
					break
				}
			}
		}
	}
	return problems
}}

// oidcActions log in to a cloud with GitHub's OIDC token unless given a stored secret;
// the token is only issued with permissions: id-token: write.
var oidcActions = map[string][]string{
	"azure/login":                           {"creds", "client-secret"},
	"aws-actions/configure-aws-credentials": {"aws-secret-access-key"},
	"google-github-actions/auth":            {"credentials_json"},
}

func oidcPermissionProblems(file string, workflow *yaml.Node) []Problem {
	var problems []Problem
	workflowLevel := idTokenWrite(get(workflow, "permissions"))
	for _, job := range mapping(get(workflow, "jobs")) {
		jobPermissions := get(job[1], "permissions")
		allowed := workflowLevel
		if jobPermissions != nil {
			allowed = idTokenWrite(jobPermissions)
		}
		steps := get(job[1], "steps")
		if steps == nil || allowed {
			continue
		}
		for _, step := range steps.Content {
			uses := get(step, "uses")
			if uses == nil {
				continue
			}
			secrets, ok := oidcActions[specs.ActionRepo(uses.Value)]
			if !ok {
				continue
			}
			withSecret := false
			for _, key := range secrets {
				if get(get(step, "with"), key) != nil {
					withSecret = true
				}
			}
			if !withSecret {
				problems = append(problems, Problem{File: file, Line: uses.Line, Message: fmt.Sprintf("job %s logs in with OIDC (%s without a stored secret), which needs permissions: {id-token: write, contents: read} on the job or workflow", job[0].Value, specs.ActionRepo(uses.Value))})
			}
		}
	}
	return problems
}

// checkoutPermissionProblems: a permissions: mapping sets every scope it does not name
// to none, so actions/checkout then needs contents: read (or write) next to it.
func checkoutPermissionProblems(file string, workflow *yaml.Node) []Problem {
	var problems []Problem
	for _, job := range mapping(get(workflow, "jobs")) {
		permissions := get(job[1], "permissions")
		if permissions == nil {
			permissions = get(workflow, "permissions")
		}
		if permissions == nil || permissions.Kind != yaml.MappingNode {
			continue
		}
		if contents := get(permissions, "contents"); contents != nil && (contents.Value == "read" || contents.Value == "write") {
			continue
		}
		for _, step := range get(job[1], "steps").Content {
			if uses := get(step, "uses"); uses != nil && specs.ActionRepo(uses.Value) == "actions/checkout" {
				problems = append(problems, Problem{File: file, Line: permissions.Line, Message: fmt.Sprintf("job %s sets permissions without contents: read, so actions/checkout cannot read a private repository; add contents: read", job[0].Value)})
				break
			}
		}
	}
	return problems
}

func idTokenWrite(permissions *yaml.Node) bool {
	if permissions == nil {
		return false
	}
	if permissions.Kind == yaml.ScalarNode {
		return permissions.Value == "write-all"
	}
	token := get(permissions, "id-token")
	return token != nil && token.Value == "write"
}
