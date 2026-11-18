package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JB0t/docgen/internal/llm"
	"github.com/JB0t/docgen/internal/specs"
)

func azureSets() []*specs.Set {
	return []*specs.Set{{
		Kind: specs.KindAnsible, Name: "azure.azcollection",
		Common: []specs.Option{{Name: "ad_user"}},
		Entries: []specs.Entry{
			{Name: "azure.azcollection.azure_rm_aksagentpool", Type: "module", Options: []specs.Option{
				{Name: "resource_group", Required: true}, {Name: "cluster_name", Required: true}, {Name: "name", Required: true},
				{Name: "count", Type: "int"}, {Name: "vm_size", Type: "str"}, {Name: "mode", Type: "str", Choices: []string{"System", "User"}},
				{Name: "state", Choices: []string{"present", "absent"}},
				{Name: "kubelet_config", Type: "dict", Sub: []specs.Option{{Name: "pod_max_pids", Type: "int"}}},
				{Name: "tags", Type: "dict", Aliases: []string{"labels"}},
			}},
			{Name: "azure.azcollection.azure_rm_aks", Type: "module", Options: []specs.Option{{Name: "name", Required: true}, {Name: "resource_group", Required: true}}},
		},
	}}
}

func runCheck(t *testing.T, check Check, files map[string]string, sets []*specs.Set, inputs []Input) []Problem {
	t.Helper()
	root := t.TempDir()
	stage, err := NewStage(root, files)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	ctx := &CheckContext{Stage: stage, Files: sortedFiles(files), Contents: files, Sets: sets, Inputs: inputs}
	return check.Run(ctx)
}

func messages(problems []Problem) string {
	var lines []string
	for _, problem := range problems {
		lines = append(lines, problem.String())
	}
	return strings.Join(lines, "\n")
}

const nodePoolPlaybook = `---
- name: Node pools
  hosts: localhost
  gather_facts: false
  vars:
    pool_defaults:
      vm_size: Standard_D4s_v5
  tasks:
    - name: Check inputs
      ansible.builtin.assert:
        that:
          - resource_group is defined
          - aks_cluster_name is defined
    - name: Create pools
      azure.azcollection.azure_rm_aksagentpool:
        resource_group: "{{ resource_group }}"
        cluster_name: "{{ aks_cluster_name }}"
        name: "{{ pool.name }}"
        node_count: "{{ pool.count }}"
        mode: Sytem
        labels: {team: infra}
        kubelet_config:
          pod_max_pid: 10
        state: "{{ pool.state | default('present') }}"
      loop: "{{ node_pools }}"
      loop_control:
        loop_var: pool
      register: created
    - name: Typo in the module
      azure.azcollection.azure_rm_aksnodepool:
        name: x
    - name: Missing required
      azure_rm_aks:
        name: "{{ created.results | length }}"
    - name: Not in the store
      community.general.whatever:
        anything: goes
    - name: Show
      ansible.builtin.debug:
        msg: "{{ tags_extra | default({}) }} {{ inventory_hostname }} {{ ansible_date_time.iso8601 }}"
`

func TestAnsibleSpecCheck(t *testing.T) {
	problems := runCheck(t, ansibleSpecCheck, map[string]string{"playbooks/pools.yml": nodePoolPlaybook}, azureSets(), nil)
	text := messages(problems)
	for _, want := range []string{
		`azure_rm_aksagentpool has no option "node_count" (did you mean "count"?)`,
		`mode must be one of System, User, not "Sytem"`,
		`azure_rm_aksagentpool.kubelet_config has no option "pod_max_pid" (did you mean "pod_max_pids"?)`,
		`there is no module azure.azcollection.azure_rm_aksnodepool in azure.azcollection (did you mean azure.azcollection.azure_rm_aksagentpool?)`,
		`azure_rm_aks is missing the required option "resource_group"`,
		`playbooks/pools.yml:19:`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"labels", "whatever", "anything", `"state"`} {
		if strings.Contains(text, unwanted) {
			t.Errorf("%q should pass (alias, unknown collection, templated choice):\n%s", unwanted, text)
		}
	}
}

func TestAnsibleInputs(t *testing.T) {
	inputs := AnsibleInputs(map[string]string{"playbooks/pools.yml": nodePoolPlaybook})
	var got []string
	for _, input := range inputs {
		name := input.Name
		if input.Optional {
			name += "?"
		}
		got = append(got, name)
	}
	if strings.Join(got, ",") != "resource_group,aks_cluster_name,node_pools,tags_extra?" {
		t.Errorf("inputs = %v", got)
	}
	role := AnsibleInputs(map[string]string{
		"roles/pool/defaults/main.yml": "---\npool_mode: User\n",
		"roles/pool/tasks/main.yml":    "---\n- name: x\n  ansible.builtin.set_fact:\n    computed: 1\n- name: y\n  ansible.builtin.debug:\n    msg: \"{{ pool_mode }} {{ computed }} {{ cluster }}\"\n  when: cluster_enabled | bool\n",
	})
	if len(role) != 2 || role[0].Name != "cluster" || role[1].Name != "cluster_enabled" {
		t.Errorf("role inputs = %+v", role)
	}
}

func TestContextInputsCheck(t *testing.T) {
	inputs := []Input{{Name: "resource_group", File: "pools.yml"}, {Name: "node_pools", File: "pools.yml"}, {Name: "tags_extra", Optional: true, File: "pools.yml"}}
	workflow := "on: {workflow_dispatch: {inputs: {resource_group: {}}}}\njobs: {run: {runs-on: ubuntu-latest, steps: [{run: 'ansible-playbook pools.yml -e resource_group=x'}]}}\n"
	text := messages(runCheck(t, contextInputsCheck, map[string]string{".github/workflows/p.yml": workflow}, nil, inputs))
	if !strings.Contains(text, "pools.yml needs node_pools") || strings.Contains(text, "resource_group") || strings.Contains(text, "tags_extra") {
		t.Errorf("context inputs:\n%s", text)
	}
}

func TestGithubChecks(t *testing.T) {
	sets := []*specs.Set{{Kind: specs.KindAction, Name: "azure/login@v2", Entries: []specs.Entry{{Name: "azure/login@v2", Type: "action", Options: []specs.Option{{Name: "client-id", Required: true}, {Name: "tenant-id"}}}}}}
	workflow := `name: pools
on:
  workflow_dispatch:
    inputs:
      resource_group: {required: true, type: string}
jobs:
  run:
    runs-on: ubuntu-latest
    steps:
      - uses: azure/login@v1
        with:
          tenant_id: ${{ secrets.TENANT }}
      - name: no action
      - run: echo "${{ inputs.resource_group }} ${{ inputs.cluster }}"
  broken:
    steps: []
`
	files := map[string]string{".github/workflows/pools.yml": workflow}
	structure := messages(runCheck(t, githubStructureCheck, files, nil, nil))
	for _, want := range []string{"inputs.cluster is used but not declared", "job broken has neither runs-on nor uses", "step 2 of job run has neither uses nor run"} {
		if !strings.Contains(structure, want) {
			t.Errorf("missing %q in:\n%s", want, structure)
		}
	}
	if strings.Contains(structure, "inputs.resource_group is used but not declared") {
		t.Errorf("declared input flagged:\n%s", structure)
	}
	actions := runCheck(t, actionInputsCheck, files, sets, nil)
	text := messages(actions)
	for _, want := range []string{`has no option "tenant_id" (did you mean "tenant-id"?)`, `is missing the required option "client-id"`, "the stored spec is for azure/login@v2"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestGitlabStructureCheck(t *testing.T) {
	pipeline := `spec:
  inputs:
    environment:
---
stages: [plan, apply]
variables:
  RG: ""
plan:
  stage: plan
  script:
    - echo $[[ inputs.environment ]] $[[ inputs.region ]]
apply:
  stage: deploy
  needs: [plan, lint]
  script: [echo]
.template:
  image: alpine
noscript:
  stage: apply
`
	text := messages(runCheck(t, gitlabStructureCheck, map[string]string{".gitlab-ci.yml": pipeline}, nil, nil))
	for _, want := range []string{`inputs.region ]] is used but not declared`, `job apply uses stage "deploy"`, `needs names "lint"`, "job noscript has no script"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, ".template") || strings.Contains(text, "inputs.environment") || strings.Contains(text, "variables") {
		t.Errorf("false positive:\n%s", text)
	}
}

func terraformSets() []*specs.Set {
	return []*specs.Set{{Kind: specs.KindTerraform, Name: "hashicorp/azurerm", Entries: []specs.Entry{{
		Name: "azurerm_kubernetes_cluster_node_pool", Type: "resource", Options: []specs.Option{
			{Name: "id", Computed: true}, {Name: "name", Required: true}, {Name: "kubernetes_cluster_id", Required: true},
			{Name: "node_count"}, {Name: "vm_size", Required: true},
			{Name: "upgrade_settings", Block: true, Sub: []specs.Option{{Name: "max_surge", Required: true}}},
			{Name: "node_network_profile", Block: true, Sub: []specs.Option{{Name: "allowed_host_ports", Block: true, Sub: []specs.Option{{Name: "port_start"}}}}},
		}}}}}
}

func TestTerraformSpecCheck(t *testing.T) {
	config := `resource "azurerm_kubernetes_cluster_node_pool" "pool" {
  for_each              = var.pools
  name                  = each.key
  kubernetes_cluster_id = var.cluster_id
  nodes_count           = each.value.count
  id                    = "x"
  upgrade_settings = {}
  node_network_profile {
    dynamic "allowed_host_ports" {
      for_each = []
      content {
        port_begin = 1
      }
    }
  }
  lifecycle {
    ignore_changes = [node_count]
  }
}

resource "azurerm_kubernetes_cluster_nodepool" "typo" {}
resource "random_id" "other" {}
`
	text := messages(runCheck(t, terraformSpecCheck, map[string]string{"infra/main.tf": config}, terraformSets(), nil))
	for _, want := range []string{
		`has no argument "nodes_count" (did you mean "node_count"?)`,
		`azurerm_kubernetes_cluster_node_pool.id is computed by the provider`,
		`upgrade_settings is a nested block`,
		`azurerm_kubernetes_cluster_node_pool.node_network_profile.allowed_host_ports has no argument "port_begin" (did you mean "port_start"?)`,
		`is missing the required argument "vm_size"`,
		`there is no resource type azurerm_kubernetes_cluster_nodepool in the stored provider schema (did you mean azurerm_kubernetes_cluster_node_pool?)`,
		"infra/main.tf:5:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "random_id") || strings.Contains(text, "for_each") || strings.Contains(text, "lifecycle") {
		t.Errorf("false positive:\n%s", text)
	}
	inputs := TerraformInputs(map[string]string{"v.tf": "variable \"a\" {}\nvariable \"b\" {\n  default = 1\n}\n"})
	if len(inputs) != 2 || inputs[0].Name != "a" || inputs[0].Optional || !inputs[1].Optional {
		t.Errorf("terraform inputs = %+v", inputs)
	}
}

func TestSyntaxCheck(t *testing.T) {
	files := map[string]string{
		"a.yml":                  "key: [unclosed\n",
		"b.json":                 "{\"a\": }",
		"c.tf":                   "resource \"x\" {",
		"chart/templates/d.yaml": "{{- if .Values.x }}\nkind: x\n{{- end }}\n",
		"ok.yml":                 "---\na: 1\n---\nb: 2\n",
	}
	text := messages(runCheck(t, syntaxCheck, files, nil, nil))
	for _, want := range []string{"a.yml: invalid YAML", "b.json: invalid JSON", "c.tf:"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "templates/d.yaml") || strings.Contains(text, "ok.yml") {
		t.Errorf("false positive:\n%s", text)
	}
}

func TestStage(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{"ansible.cfg": "[defaults]\n", "roles/base/tasks/main.yml": "---\n", "playbooks/old.yml": "---\n"} {
		full := filepath.Join(root, path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(content), 0o644)
	}
	_ = os.Mkdir(filepath.Join(root, ".git"), 0o755)
	stage, err := NewStage(root, map[string]string{"playbooks/new.yml": "---\n- hosts: all\n"})
	if err != nil {
		t.Fatal(err)
	}
	dir := stage.Dir
	for path, wantLink := range map[string]bool{"ansible.cfg": true, "roles": true, "playbooks/old.yml": true, "playbooks/new.yml": false, "playbooks": false} {
		info, err := os.Lstat(filepath.Join(dir, path))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if isLink := info.Mode()&os.ModeSymlink != 0; isLink != wantLink {
			t.Errorf("%s: symlink %v, want %v", path, isLink, wantLink)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		t.Error(".git should not be linked into the stage")
	}
	if got := stage.Clean("ERROR in " + filepath.Join(dir, "playbooks/new.yml") + " and " + dir); got != "ERROR in playbooks/new.yml and ." {
		t.Errorf("clean = %q", got)
	}
	if ansibleConfigDir(stage, "playbooks/new.yml") != "." {
		t.Error("ansible.cfg at the root")
	}
	stage.Close()
	if _, err := os.Stat(filepath.Join(root, "ansible.cfg")); err != nil {
		t.Error("closing the stage must not touch the real files")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("stage not removed")
	}
}

func TestCommandCheck(t *testing.T) {
	check := commandCheck("grep -L TODO {files} | grep . && exit 1 || exit 0")
	problems := runCheck(t, check, map[string]string{"a.txt": "TODO: x\n", "b.txt": "done\n"}, nil, nil)
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "b.txt") {
		t.Errorf("problems = %+v", problems)
	}
}

func TestScaffold(t *testing.T) {
	if got := strings.Join(ansibleKind.Scaffold("ansible/roles/aks_pool"), " "); got != "ansible-galaxy role init --init-path ansible/roles aks_pool" {
		t.Errorf("role scaffold = %q", got)
	}
	if ansibleKind.Scaffold("playbooks/x.yml") != nil {
		t.Error("only roles scaffold")
	}
	if got := strings.Join(helmKind.Scaffold("charts/web/"), " "); got != "helm create charts/web" {
		t.Errorf("helm scaffold = %q", got)
	}
	if kind, err := FindKind("gha"); err != nil || kind != githubKind {
		t.Error("alias")
	}
}

// fakeModel answers from a script, recording what it was sent.
type fakeModel struct {
	answers []string
	sent    [][]llm.Message
}

func (f *fakeModel) Chat(messages []llm.Message, maxTokens int) (string, llm.Stats, error) {
	f.sent = append(f.sent, append([]llm.Message(nil), messages...))
	answer := f.answers[0]
	f.answers = f.answers[1:]
	return answer, llm.Stats{}, nil
}

func TestGenerateFixesFailedChecks(t *testing.T) {
	root := t.TempDir()
	model := &fakeModel{answers: []string{
		// Round 1: outside the allowed files.
		"secrets.yml\n<<<<<<< SEARCH\n=======\na: 1\n>>>>>>> REPLACE\n",
		// Round 2: the playbook, with a wrong option.
		"playbooks/pools.yml\n<<<<<<< SEARCH\n=======\n---\n- hosts: localhost\n  tasks:\n    - name: Pool\n      azure.azcollection.azure_rm_aksagentpool:\n        resource_group: rg\n        cluster_name: c\n        name: p\n        node_count: 2\n>>>>>>> REPLACE\n",
		// Round 3: a fix block against the current file.
		"playbooks/pools.yml\n<<<<<<< SEARCH\n        node_count: 2\n=======\n        count: 2\n>>>>>>> REPLACE\n",
	}}
	plan := &Plan{Kind: &Kind{Name: "ansible", Title: "Ansible", Checks: []Check{syntaxCheck, ansibleSpecCheck}}, Request: "pools", Out: []string{"playbooks"}}
	plan.Build()
	result, err := Generate(model, Session{Root: root, Plan: plan, Sets: azureSets(), Retries: 2, MaxResponse: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Report.Problems) != 0 || result.Rounds != 3 {
		t.Fatalf("rounds %d, problems %v", result.Rounds, result.Report.Problems)
	}
	if !strings.Contains(result.Contents["playbooks/pools.yml"], "        count: 2\n") || !strings.Contains(result.Diff, "+++ b/playbooks/pools.yml") {
		t.Errorf("result:\n%s\n%s", result.Contents["playbooks/pools.yml"], result.Diff)
	}
	second := model.sent[1][len(model.sent[1])-1].Content
	if !strings.Contains(second, "secrets.yml is outside the files you may write") {
		t.Errorf("round 2 feedback:\n%s", second)
	}
	third := model.sent[2][len(model.sent[2])-1].Content
	if !strings.Contains(third, `has no option "node_count"`) || !strings.Contains(third, `<file path="playbooks/pools.yml">`) {
		t.Errorf("round 3 feedback should list the problem and the current file:\n%s", third)
	}
	if _, err := os.Stat(filepath.Join(root, "playbooks")); err == nil {
		t.Error("Generate must not write to the tree")
	}
}

func TestPickSpecsBudget(t *testing.T) {
	set := &specs.Set{Kind: specs.KindDocs, Name: "d"}
	long := strings.Repeat("word ", 900)
	set.Entries = []specs.Entry{{Name: "big", Type: "doc", Text: long}, {Name: "small", Type: "doc", Text: "short text"}, {Name: "other", Type: "doc", Text: "x"}}
	ranked := []specs.Hit{{Set: set, Entry: &set.Entries[0], Score: 1}, {Set: set, Entry: &set.Entries[1], Score: 0.9}, {Set: set, Entry: &set.Entries[2], Score: 0.8}}
	picked, _ := PickSpecs(nil, ranked, 2, 400)
	if len(picked) != 2 || !picked[0].Compact || picked[1].Entry.Name != "small" {
		t.Errorf("picked = %+v", picked)
	}
}

func TestPlanPrompt(t *testing.T) {
	plan := &Plan{Kind: githubKind, Request: "run it", Out: []string{".github/workflows/p.yml"}, Create: []string{".github/workflows/p.yml"},
		Context: map[string]string{"pools.yml": nodePoolPlaybook}}
	plan.Inputs = ContextInputs(plan.Context)
	plan.Build()
	for _, want := range []string{"pass every one): resource_group, aks_cluster_name, node_pools", "Optional values (they have defaults): tags_extra", "Create (empty SEARCH part): .github/workflows/p.yml", "<<<<<<< SEARCH"} {
		if !strings.Contains(plan.Prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, plan.Prompt)
		}
	}
	if !strings.Contains(plan.System, "GitHub Actions workflows") {
		t.Errorf("system: %s", plan.System)
	}
}

func TestGenerateAcceptsMissingPathForOneFile(t *testing.T) {
	model := &fakeModel{answers: []string{"<<<<<<< SEARCH\n=======\n---\na: 1\n>>>>>>> REPLACE\n"}}
	plan := &Plan{Kind: &Kind{Name: "any", Title: "files", Checks: []Check{syntaxCheck}}, Request: "x", Out: []string{"conf/a.yml"}, Create: []string{"conf/a.yml"}}
	plan.Build()
	if !strings.Contains(plan.Prompt, "conf/a.yml\n<<<<<<< SEARCH") {
		t.Errorf("prompt should show the path line with the real path:\n%s", plan.Prompt)
	}
	result, err := Generate(model, Session{Root: t.TempDir(), Plan: plan, MaxResponse: 10})
	if err != nil || result.Contents["conf/a.yml"] != "---\na: 1\n" {
		t.Fatalf("result %+v, %v", result, err)
	}
	two := &Plan{Out: []string{"conf"}}
	if two.onlyFile(nil) != "" {
		t.Error("a directory target has no single file")
	}
}

func TestGithubSecurityChecks(t *testing.T) {
	workflow := `on:
  workflow_dispatch:
    inputs:
      rg: {type: string}
jobs:
  oidc:
    runs-on: ubuntu-latest
    steps:
      - uses: azure/login@v2
        with: {client-id: x, tenant-id: y}
      - run: echo "${{ inputs.rg }}"
  secret:
    runs-on: ubuntu-latest
    steps:
      - uses: azure/login@v2
        with: {creds: "${{ secrets.AZ }}"}
      - run: echo "$RG"
        env: {RG: "${{ inputs.rg }}"}
  allowed:
    runs-on: ubuntu-latest
    permissions: {id-token: write, contents: read}
    steps:
      - uses: azure/login@v2
`
	text := messages(runCheck(t, githubStructureCheck, map[string]string{"w.yml": workflow}, nil, nil))
	if !strings.Contains(text, "job oidc logs in with OIDC") || strings.Contains(text, "job secret logs in") || strings.Contains(text, "job allowed logs in") {
		t.Errorf("OIDC permission check:\n%s", text)
	}
	if !strings.Contains(text, "job oidc: a run: script expands ${{ inputs.rg }} directly") || strings.Contains(text, "job secret: a run") {
		t.Errorf("injection check:\n%s", text)
	}
}

func TestStructuredInputsAndCheckoutPermissions(t *testing.T) {
	inputs := AnsibleInputs(map[string]string{"pools.yml": nodePoolPlaybook})
	structured := map[string]bool{}
	for _, input := range inputs {
		structured[input.Name] = input.Structured
	}
	if !structured["node_pools"] || structured["resource_group"] {
		t.Fatalf("looped variables are structured: %+v", inputs)
	}
	workflow := `on: {workflow_dispatch: {inputs: {rg: {type: string}}}}
jobs:
  run:
    runs-on: ubuntu-latest
    permissions: {id-token: write}
    steps:
      - uses: actions/checkout@v4
      - run: ansible-playbook pools.yml -e resource_group="$RG" -e node_pools="$POOLS" -e aks_cluster_name=x
`
	files := map[string]string{"w.yml": workflow}
	text := messages(runCheck(t, contextInputsCheck, files, nil, inputs))
	if !strings.Contains(text, "passes node_pools as -e node_pools=...") || strings.Contains(text, "passes resource_group") {
		t.Errorf("structured input check:\n%s", text)
	}
	fixed := map[string]string{"w.yml": strings.Replace(workflow, `-e node_pools="$POOLS"`, `-e "{\"node_pools\": $POOLS}"`, 1)}
	if text := messages(runCheck(t, contextInputsCheck, fixed, nil, inputs)); text != "" {
		t.Errorf("JSON extra vars should pass:\n%s", text)
	}
	text = messages(runCheck(t, githubStructureCheck, files, nil, nil))
	if !strings.Contains(text, "job run sets permissions without contents: read") {
		t.Errorf("checkout permission check:\n%s", text)
	}
	files["w.yml"] = strings.Replace(workflow, "{id-token: write}", "{id-token: write, contents: read}", 1)
	if text := messages(runCheck(t, githubStructureCheck, files, nil, nil)); strings.Contains(text, "contents: read") {
		t.Errorf("contents: read given:\n%s", text)
	}
}
