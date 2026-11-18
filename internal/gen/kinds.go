// Package gen generates new files (boilerplate a developer then finishes) with the local
// model: it retrieves the specs of the modules, resources or actions a request needs,
// shows the model the project's own conventions and the files the request builds on,
// and checks the answer with real tools and against the specs before anything is
// written, sending failures back for a fix.
package gen

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/JB0t/docgen/internal/specs"
)

// Kind is a kind of file docgen gen writes: what to retrieve, how to instruct the
// model, how to scaffold, and which checks run.
type Kind struct {
	Name    string
	Aliases []string
	Title   string // for prompts: "Ansible playbooks, roles and task files"
	// SpecKinds are the spec kinds retrieved for it (docs sets tagged for it always are).
	SpecKinds []string
	Guide     string
	// Scaffold returns the command that creates a skeleton at out, when the kind has
	// a generator for it (ansible-galaxy role init, helm create).
	Scaffold func(out string) []string
	Checks   []Check
}

// Kinds are the built-in kinds.
var Kinds = []*Kind{ansibleKind, githubKind, gitlabKind, terraformKind, helmKind, anyKind}

// FindKind returns a kind by name or alias.
func FindKind(name string) (*Kind, error) {
	for _, kind := range Kinds {
		if kind.Name == name {
			return kind, nil
		}
		for _, alias := range kind.Aliases {
			if alias == name {
				return kind, nil
			}
		}
	}
	var names []string
	for _, kind := range Kinds {
		names = append(names, kind.Name)
	}
	return nil, fmt.Errorf("unknown kind %q (use: %s)", name, strings.Join(names, ", "))
}

var ansibleKind = &Kind{
	Name:      "ansible",
	Title:     "Ansible playbooks, roles and task files",
	SpecKinds: []string{specs.KindAnsible, specs.KindDocs},
	Guide: `- Start every YAML file with ---. Use fully qualified module names (ansible.builtin.copy, azure.azcollection.azure_rm_aks).
- Use only modules and options shown in the reference specs when they cover the task; never invent option names.
- Values the caller must supply are variables that the playbook does not define: check them in a first task with ansible.builtin.assert (that: name is defined). Optional settings use | default(...).
- Name every task. Use true/false for booleans. Lists of similar things (several node pools, users, rules) are a list variable and one task with loop.
- In a role, defaults go in defaults/main.yml and tasks in tasks/main.yml; keep meta/main.yml valid.`,
	Scaffold: func(out string) []string {
		parent, name := path.Split(strings.TrimSuffix(out, "/"))
		if path.Base(strings.TrimSuffix(parent, "/")) != "roles" || name == "" {
			return nil
		}
		return []string{"ansible-galaxy", "role", "init", "--init-path", strings.TrimSuffix(parent, "/"), name}
	},
	Checks: []Check{syntaxCheck, ansibleSpecCheck, ansibleSyntaxCheck, ansibleLintCheck},
}

var githubKind = &Kind{
	Name:      "github-actions",
	Aliases:   []string{"github", "gha", "actions"},
	Title:     "GitHub Actions workflows",
	SpecKinds: []string{specs.KindAction, specs.KindDocs},
	Guide: `- Workflows live in .github/workflows/*.yml. Use the action versions shown in the specs, and only their listed inputs under with:.
- Values a person provides when running it are on.workflow_dispatch.inputs (with description, required, type) and are read as ${{ inputs.NAME }}; secrets come from ${{ secrets.NAME }}, never literals.
- Do not put ${{ }} expressions inside run: scripts directly; map them to env: variables and use "$VAR" in the script.
- When the workflow runs a playbook or other files from the context, pass every input they expect (listed under Context).
- Pass list and dict values to ansible-playbook as JSON extra vars (-e "{\"pools\": $POOLS}" with POOLS a JSON env value); -e name=value always makes a string.
- Logging in with OIDC (azure/login, aws-actions/configure-aws-credentials, google-github-actions/auth without stored secrets) needs permissions: id-token: write.`,
	Checks: []Check{syntaxCheck, githubStructureCheck, actionInputsCheck, contextInputsCheck, actionlintCheck},
}

var gitlabKind = &Kind{
	Name:      "gitlab-ci",
	Aliases:   []string{"gitlab"},
	Title:     "GitLab CI/CD pipelines",
	SpecKinds: []string{specs.KindDocs},
	Guide: `- The pipeline is .gitlab-ci.yml (or the file asked for). Declare stages:; every job has a stage and a script (or trigger or extends).
- Values a person provides when running it are variables with a description (shown in Run pipeline) or spec: inputs; secrets are masked CI/CD variables, never literals.
- When a job runs a playbook or other files from the context, pass every input they expect (listed under Context).
- Pass list and dict values to ansible-playbook as JSON extra vars (-e "{\"pools\": $POOLS}"); -e name=value always makes a string.`,
	Checks: []Check{syntaxCheck, gitlabStructureCheck, contextInputsCheck},
}

var terraformKind = &Kind{
	Name:      "terraform",
	Aliases:   []string{"tf"},
	Title:     "Terraform configurations and modules",
	SpecKinds: []string{specs.KindTerraform, specs.KindDocs},
	Guide: `- Use only resource types, arguments and blocks shown in the reference specs; never invent argument names, and never set computed-only attributes.
- Inputs are variable blocks with type and description (in variables.tf), results are output blocks (in outputs.tf), and the terraform block pins required_providers with version constraints (in versions.tf).
- Several similar resources are one resource with for_each over a map variable.`,
	Checks: []Check{syntaxCheck, terraformFmtCheck, terraformSpecCheck, terraformValidateCheck},
}

var helmKind = &Kind{
	Name:      "helm",
	Title:     "Helm charts",
	SpecKinds: []string{specs.KindDocs},
	Guide: `- A chart has Chart.yaml (apiVersion: v2), values.yaml and templates/; use the template names defined in templates/_helpers.tpl.
- Every configurable value is in values.yaml with a comment; templates read it from .Values. Use stable Kubernetes API versions.`,
	Scaffold: func(out string) []string {
		return []string{"helm", "create", strings.TrimSuffix(out, "/")}
	},
	Checks: []Check{syntaxCheck, helmLintCheck, helmTemplateCheck},
}

var anyKind = &Kind{
	Name:      "any",
	Title:     "files",
	SpecKinds: []string{specs.KindDocs},
	Guide:     `- Follow the reference docs and the examples' conventions.`,
	Checks:    []Check{syntaxCheck},
}

// KindNames lists kind names and aliases, for help text.
func KindNames() string {
	var names []string
	for _, kind := range Kinds {
		name := kind.Name
		if len(kind.Aliases) > 0 {
			name += " (" + strings.Join(kind.Aliases, ", ") + ")"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
