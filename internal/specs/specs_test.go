package specs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T) []Entry {
	t.Helper()
	data, err := os.ReadFile("testdata/ansible-doc-aksagentpool.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ParseAnsibleDoc(data)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func option(options []Option, name string) *Option {
	for index := range options {
		if options[index].Name == name {
			return &options[index]
		}
	}
	return nil
}

func TestParseAnsibleDoc(t *testing.T) {
	entries := loadFixture(t)
	if len(entries) != 1 || entries[0].Name != "azure.azcollection.azure_rm_aksagentpool" || entries[0].Type != "module" {
		t.Fatalf("entries = %+v", entries)
	}
	entry := entries[0]
	if entry.Summary != "Manage node pools in Kubernetes kubernetes cluster" {
		t.Errorf("summary = %q", entry.Summary)
	}
	if resourceGroup := option(entry.Options, "resource_group"); resourceGroup == nil || !resourceGroup.Required || resourceGroup.Type != "str" {
		t.Errorf("resource_group = %+v", resourceGroup)
	}
	if mode := option(entry.Options, "mode"); mode == nil || strings.Join(mode.Choices, ",") != "System,User" {
		t.Errorf("mode = %+v", mode)
	}
	if zones := option(entry.Options, "availability_zones"); zones == nil || strings.Join(zones.Choices, ",") != "1,2,3" || zones.Elements != "int" {
		t.Errorf("availability_zones = %+v", zones)
	}
	if kubelet := option(entry.Options, "kubelet_config"); kubelet == nil || len(kubelet.Sub) != 3 {
		t.Errorf("kubelet_config = %+v", kubelet)
	}
	if source := option(entry.Options, "auth_source"); source == nil || strings.Contains(source.Desc, "C(") || source.Default != "auto" {
		t.Errorf("auth_source = %+v (markup should be cleaned, first sentence only)", source)
	}
	if !strings.HasPrefix(entry.Example, "- name: Add new node agent pool") || strings.Count(entry.Example, "- name:") != 1 {
		t.Errorf("example should be the first task only:\n%s", entry.Example)
	}
}

func TestCommonOptions(t *testing.T) {
	var entries []Entry
	for index := 0; index < 12; index++ {
		options := []Option{{Name: "ad_user"}, {Name: "profile"}, {Name: "name", Required: true}}
		if index == 0 {
			options = append(options, Option{Name: "count"})
		}
		entries = append(entries, Entry{Name: "c.c.m" + itoa(index), Options: options})
	}
	var names []string
	for _, option := range CommonOptions(entries) {
		names = append(names, option.Name)
	}
	if strings.Join(names, ",") != "ad_user,profile" {
		t.Errorf("common = %v (required options are never common)", names)
	}
	if CommonOptions(entries[:5]) != nil {
		t.Error("small sets have no common options")
	}
}

func TestRenderSkipsCommonOptions(t *testing.T) {
	entries := loadFixture(t)
	set := &Set{Kind: KindAnsible, Name: "azure.azcollection", Common: []Option{{Name: "ad_user"}, {Name: "auth_source"}}, Entries: entries}
	text := Render(set, &set.Entries[0], false)
	if strings.Contains(text, "ad_user") || !strings.Contains(text, "- resource_group* (str)") || !strings.Contains(text, "    - allowed_unsafe_sysctls") {
		t.Errorf("render:\n%s", text)
	}
	if !strings.Contains(text, "choices: System|User") || !strings.Contains(text, "```yaml") {
		t.Errorf("render lacks choices or example:\n%s", text)
	}
	compact := Render(set, &set.Entries[0], true)
	if len(compact) >= len(text) || strings.Contains(compact, "```") {
		t.Errorf("compact render should be shorter and have no example:\n%s", compact)
	}
	if !strings.Contains(RenderCommon(set), "ad_user, auth_source") {
		t.Errorf("common note: %s", RenderCommon(set))
	}
}

const tfSchemaJSON = `{"format_version":"1.0","provider_schemas":{"registry.terraform.io/hashicorp/azurerm":{
 "resource_schemas":{
  "azurerm_kubernetes_cluster_node_pool":{"block":{"description":"Manages a Node Pool within a Kubernetes Cluster.","attributes":{
     "id":{"type":"string","computed":true},
     "name":{"type":"string","required":true,"description":"The name of the Node Pool."},
     "kubernetes_cluster_id":{"type":"string","required":true},
     "node_count":{"type":"number","optional":true,"computed":true},
     "node_labels":{"type":["map","string"],"optional":true},
     "old":{"type":"string","optional":true,"deprecated":true}},
   "block_types":{"upgrade_settings":{"nesting_mode":"list","max_items":1,"block":{"attributes":{"max_surge":{"type":"string","required":true}}}}}}},
  "azurerm_resource_group":{"block":{"attributes":{"name":{"type":"string","required":true}}}}},
 "data_source_schemas":{"azurerm_kubernetes_cluster":{"block":{"attributes":{"name":{"type":"string","required":true},"fqdn":{"type":"string","computed":true}}}}}}}}`

func TestParseTerraformSchema(t *testing.T) {
	sets, err := ParseTerraformSchema([]byte(tfSchemaJSON), []string{"azurerm_kubernetes_*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || sets[0].Name != "hashicorp/azurerm" || len(sets[0].Entries) != 2 {
		t.Fatalf("sets = %+v", sets)
	}
	var pool *Entry
	for index := range sets[0].Entries {
		if sets[0].Entries[index].Name == "azurerm_kubernetes_cluster_node_pool" {
			pool = &sets[0].Entries[index]
		}
	}
	if pool == nil || pool.Type != "resource" {
		t.Fatalf("node pool entry missing: %+v", sets[0].Entries)
	}
	if id := option(pool.Options, "id"); id == nil || !id.Computed {
		t.Errorf("id should be computed-only: %+v", id)
	}
	if count := option(pool.Options, "node_count"); count == nil || count.Computed || count.Type != "number" {
		t.Errorf("node_count is optional+computed, so writable: %+v", count)
	}
	if labels := option(pool.Options, "node_labels"); labels == nil || labels.Type != "map(string)" {
		t.Errorf("node_labels = %+v", labels)
	}
	if option(pool.Options, "old") != nil {
		t.Error("deprecated optional attributes are dropped")
	}
	if upgrade := option(pool.Options, "upgrade_settings"); upgrade == nil || !upgrade.Block || upgrade.Required || len(upgrade.Sub) != 1 {
		t.Errorf("upgrade_settings = %+v", upgrade)
	}
	text := Render(sets[0], pool, false)
	if strings.Contains(text, "- id") || !strings.Contains(text, "- name* (string)") || !strings.Contains(text, "- upgrade_settings (block, list)") {
		t.Errorf("render:\n%s", text)
	}
}

func TestParseAction(t *testing.T) {
	entry, err := ParseAction("azure/login@v2", []byte(`name: Azure Login
description: Authenticate to Azure and run your az CLI or Az PowerShell based actions or scripts.
inputs:
  creds:
    description: 'Paste output of az ad sp create-for-rbac'
    required: false
  client-id:
    description: ClientId of the Azure Service principal created.
    required: true
  enable-AzPSSession:
    description: Set this value to true to enable Azure PowerShell Login
    required: true
    default: false
  old:
    deprecationMessage: gone
outputs:
  token:
    description: the token`))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Summary != "Azure Login: Authenticate to Azure and run your az CLI or Az PowerShell based actions or scripts." {
		t.Errorf("summary = %q", entry.Summary)
	}
	if clientID := option(entry.Options, "client-id"); clientID == nil || !clientID.Required {
		t.Errorf("client-id = %+v", clientID)
	}
	if session := option(entry.Options, "enable-AzPSSession"); session == nil || session.Required || session.Default != "false" {
		t.Errorf("an input with a default is not required: %+v", session)
	}
	if option(entry.Options, "old") != nil || len(entry.Outputs) != 1 {
		t.Errorf("options %+v outputs %+v", entry.Options, entry.Outputs)
	}
	if !ActionRef.MatchString("actions/checkout@v4") || !ActionRef.MatchString("github/codeql-action/init@v3") || ActionRef.MatchString("checkout") {
		t.Error("ActionRef")
	}
}

func TestDocEntries(t *testing.T) {
	text := "# Values\n\nintro text that is long enough to be kept as a chunk\n\n## ingress\n\nenabled: true is the switch for the ingress resource\n\n## ingress\n\nsecond section with the same title, kept apart\n"
	entries := DocEntries("chart", text)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	want := "chart: Values|chart: Values > ingress|chart: Values > ingress (2)"
	if strings.Join(names, "|") != want {
		t.Errorf("names = %q, want %q", strings.Join(names, "|"), want)
	}
}

func azureSet() *Set {
	return &Set{Kind: KindAnsible, Name: "azure.azcollection", Entries: []Entry{
		{Name: "azure.azcollection.azure_rm_aksagentpool", Type: "module", Summary: "Manage node pool instance", Options: []Option{{Name: "resource_group"}, {Name: "cluster_name"}, {Name: "count"}}},
		{Name: "azure.azcollection.azure_rm_aks", Type: "module", Summary: "Manage a managed Azure Container Service (ACS) instance", Options: []Option{{Name: "resource_group"}, {Name: "agent_pool_profiles"}, {Name: "node_resource_group"}}},
		{Name: "azure.azcollection.azure_rm_aksagentpool_info", Type: "module", Summary: "Show node pool info", Options: []Option{{Name: "resource_group"}}},
		{Name: "azure.azcollection.azure_rm_storageaccount", Type: "module", Summary: "Manage Azure storage accounts", Options: []Option{{Name: "resource_group"}}},
	}}
}

func TestSearchKeywords(t *testing.T) {
	sets := []*Set{azureSet(), {Kind: KindDocs, Name: "helm-notes", For: "helm", Entries: []Entry{{Name: "helm-notes: pools", Type: "doc", Text: "node pool aks"}}}}
	terms := []string{"provision", "aks", "node", "pools", "resource", "group"}
	hits := Search(sets, Filter{Kinds: []string{KindAnsible, KindDocs}, For: "ansible"}, terms, nil, 10)
	if len(hits) == 0 || hits[0].Entry.Name != "azure.azcollection.azure_rm_aksagentpool" {
		t.Fatalf("top hit = %+v", hits)
	}
	for _, hit := range hits {
		if hit.Set.Name == "helm-notes" {
			t.Error("docs for another kind must not be searched")
		}
	}
	rank := map[string]int{}
	for index, hit := range hits {
		rank[hit.Entry.Name] = index
	}
	if rank["azure.azcollection.azure_rm_aksagentpool_info"] < rank["azure.azcollection.azure_rm_aks"] {
		t.Errorf("_info modules rank below managing ones unless asked: %v", rank)
	}
}

func TestStoreEmbedAndSearchByMeaning(t *testing.T) {
	dir := t.TempDir()
	set := azureSet()
	calls := 0
	embed := func(texts []string) ([][]float32, error) {
		calls += len(texts)
		var vectors [][]float32
		for _, text := range texts {
			switch {
			case strings.Contains(text, "search_query"), strings.Contains(text, "aksagentpool\n"):
				vectors = append(vectors, []float32{1, 0})
			default:
				vectors = append(vectors, []float32{0, 1})
			}
		}
		return vectors, nil
	}
	if count, err := set.Embed("nomic", embed); err != nil || count != 4 {
		t.Fatalf("embedded %d, %v", count, err)
	}
	if err := set.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || len(loaded) != 1 || loaded[0].Unembedded("nomic") != 0 || loaded[0].Unembedded("other") != 4 {
		t.Fatalf("loaded %+v, %v", loaded, err)
	}
	if filepath.Base(loaded[0].Path()) != "azure.azcollection.json" {
		t.Errorf("path = %s", loaded[0].Path())
	}
	// Refreshing keeps the vectors of unchanged entries and embeds only the new one.
	refreshed := azureSet()
	refreshed.Entries = append(refreshed.Entries, Entry{Name: "azure.azcollection.azure_rm_new", Type: "module"})
	refreshed.KeepVectors(loaded[0])
	calls = 0
	if count, _ := refreshed.Embed("nomic", embed); count != 1 || calls != 1 {
		t.Errorf("refresh embedded %d (%d calls), want 1", count, calls)
	}
	query, _ := embed([]string{"search_query: x"})
	hits := Search(loaded, Filter{}, nil, query[0], 2)
	if len(hits) == 0 || hits[0].Entry.Name != "azure.azcollection.azure_rm_aksagentpool" || hits[0].Meaning < 0.99 {
		t.Errorf("meaning search: %+v", hits)
	}
}

func TestLookup(t *testing.T) {
	sets := []*Set{azureSet(), {Kind: KindAction, Name: "actions/checkout@v4", Entries: []Entry{{Name: "actions/checkout@v4", Type: "action"}}}}
	if hits, err := Lookup(sets, "azure_rm_aks"); err != nil || hits[0].Entry.Name != "azure.azcollection.azure_rm_aks" {
		t.Errorf("short name: %+v %v", hits, err)
	}
	if hits, err := Lookup(sets, "actions/checkout@v4"); err != nil || len(hits) != 1 {
		t.Errorf("set name: %+v %v", hits, err)
	}
	if _, err := Lookup(sets, "azure_rm_aksagentpol"); err == nil || !strings.Contains(err.Error(), "azure_rm_aksagentpool") {
		t.Errorf("a typo should suggest the close name: %v", err)
	}
}

func TestClosest(t *testing.T) {
	names := []string{"count", "max_count", "min_count", "vm_size", "mode"}
	for input, want := range map[string]string{"node_count": "count", "vmsize": "vm_size", "modes": "mode", "zebra": ""} {
		if got := Closest(input, names); got != want {
			t.Errorf("Closest(%q) = %q, want %q", input, got, want)
		}
	}
}
