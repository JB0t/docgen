package facts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/extract"
)

func find(f *Facts, language, name string) *extract.Symbol {
	for index, symbol := range f.Symbols {
		if symbol.Language == language && symbol.Name == name {
			return &f.Symbols[index]
		}
	}
	return nil
}

func TestBuildAllLanguages(t *testing.T) {
	root, _ := filepath.Abs("../extract/testdata")
	cfg, _ := config.Load(root)
	f, err := Build(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		language, name, kind, doc, parent string
		exported                          bool
	}{
		{"go", "Greet", "method", "Greet returns a greeting.", "Greeter", true},
		{"go", "helper", "function", "", "", false},
		{"python", "fetch", "method", "Fetch one item.", "Client", true},
		{"python", "_private", "function", "", "", false},
		{"terraform", "cluster_name", "variable", "Name of the AKS cluster", "", true},
		{"terraform", "azurerm_resource_group.main", "resource", "", "", true},
		{"terraform", "network", "module", "", "", true},
		{"terraform", "node_count", "tfvar", "", "", true},
		{"terraform", "region", "tfvar", "Production cluster.", "", true},
		{"terraform", "vpc", "dependency", "", "", true},
		{"terraform", "vpc_id", "input", "", "", true},
		{"bash", "log", "function", "Prints a message.", "", true},
		{"bash", "--dry-run", "flag", "", "deploy.sh", true},
		{"bash", "--verbose", "flag", "", "deploy.sh", true},
		{"csharp", "BondPricer", "class", "Prices bonds.", "", true},
		{"csharp", "CleanPrice", "method", "Returns the clean price.", "BondPricer", true},
		{"rust", "Portfolio", "struct", "A portfolio of positions.", "", true},
		{"rust", "exposure", "method", "Total exposure.", "Portfolio", true},
		{"rust", "internal", "function", "", "", false},
		{"r", "rolling_vol", "function", "Rolling volatility\n\n", "", true},
		{"helm", "orchestrator", "chart", "Dagster orchestrator chart", "", true},
		{"helm", "image.repository", "value", "Image repository.", "", true},
		{"ansible", "web_port", "variable", "Port nginx listens on.", "web", true},
		{"ansible", "Install nginx", "task", "", "", true},
		{"ansible", "Deploy web", "play", "Deploys the web role: a play with roles only (no tasks:), as many deploy playbooks are.", "", true},
		{"ansible", "Wait for the web port", "task", "", "", true},
		{"yaml", "environment", "input", "Target environment", "", true},
	}
	for _, want := range cases {
		got := find(f, want.language, want.name)
		if got == nil {
			t.Errorf("%s %s not found", want.language, want.name)
			continue
		}
		if got.Kind != want.kind || got.Parent != want.parent || got.Exported != want.exported {
			t.Errorf("%s %s = kind %q parent %q exported %v", want.language, want.name, got.Kind, got.Parent, got.Exported)
		}
		if want.doc != "" && got.Doc != want.doc && !(want.language == "r" && got.Doc != "") {
			t.Errorf("%s %s doc = %q, want %q", want.language, want.name, got.Doc, want.doc)
		}
	}
	if find(f, "csharp", "Hidden") != nil && find(f, "csharp", "Hidden").Exported {
		t.Error("private C# methods must not be exported")
	}
	for _, env := range []string{"GREETING_SUFFIX", "SAMPLE_TOKEN", "DEPLOY_ENV", "PRICER_KEY", "RISK_LIMIT", "VOL_SOURCE"} {
		if !f.HasEnvVar(env) {
			t.Errorf("env var %s not found", env)
		}
	}
}

func TestParseHelp(t *testing.T) {
	cobra := "Usage:\n  clctl tasks [command]\n\nAvailable Commands:\n  list        Tree of open tasks\n  make-a-list Longest name, one space\n\nFlags:\n  -h, --help   help for tasks\n      --refresh   refresh\n"
	subs, flags := parseHelp(cobra)
	if len(subs) != 2 || subs[0].name != "list" || subs[1].name != "make-a-list" || subs[1].short != "Longest name, one space" {
		t.Fatalf("cobra subcommands = %+v", subs)
	}
	if len(flags) != 3 {
		t.Fatalf("cobra flags = %v", flags)
	}
	argparse := "usage: tool [-h] {plan,apply} ...\n\npositional arguments:\n  {plan,apply}\n    plan      Show a plan\n\noptions:\n  -h, --help  show help\n  --workspace WORKSPACE\n"
	subs, flags = parseHelp(argparse)
	if len(subs) != 2 || subs[1].name != "apply" || len(flags) != 3 {
		t.Fatalf("argparse = %+v %v", subs, flags)
	}
}

func TestValueFlagsAndArgs(t *testing.T) {
	help := "Usage:\n  tool tasks complete [flags]\n\nFlags:\n  -n, --limit int   number\n      --task-id string   task\n      --dry-run   print\n"
	got := valueFlags(help)
	if strings.Join(got, " ") != "--limit --task-id -n" {
		t.Fatalf("valueFlags = %v", got)
	}
	if !noArgs(help, "tool", []string{"tasks", "complete"}) {
		t.Fatal("complete [flags] takes no args")
	}
	if noArgs("Usage:\n  tool make-workspace [TASK-ID] [flags]\n", "tool", []string{"make-workspace"}) {
		t.Fatal("make-workspace [TASK-ID] takes an argument")
	}
}

func TestFlagsOnlyFromDefinitions(t *testing.T) {
	help := "Write a digest.\n\n--summarize adds bullets from a local Ollama-compatible model\n\nUsage:\n  tool digest [flags]\n\nFlags:\n  -o, --output string   output file, or - for stdout\n      --summarize       add summaries\n  -n, --limit int       at most this many\n      --select string[=\" \"]   positions like 1,3-4\n"
	_, flags := parseHelp(help)
	types := valueTypes(help)
	if len(flags) != 6 || types["--select"] != "string" || types["--summarize"] != "" || types["-o"] != "string" || types["--limit"] != "int" {
		t.Fatalf("flags %v, types %v", flags, types)
	}
}

func TestClickHelp(t *testing.T) {
	help := "Usage: devlab [OPTIONS] COMMAND [ARGS]...\n\n  Dev Lab\n\nOptions:\n  --profile [local|ci]  Defaults for local use or CI pipelines\n  -v, --verbose         Verbose\n  --monitoring / --no-monitoring  Install monitoring\n  --name TEXT           A name\n  --help                Show this message and exit.\n\nCommands:\n  up    Start the lab\n  down  Stop it\n"
	subs, flags := parseHelp(help)
	types := valueTypes(help)
	if len(subs) != 2 || subs[0].name != "up" || len(flags) != 7 || types["--no-monitoring"] != "" {
		t.Fatalf("subcommands %+v flags %v", subs, flags)
	}
	if types["--profile"] != "[local|ci]" || types["--name"] != "TEXT" || types["--verbose"] != "" {
		t.Fatalf("types %v", types)
	}
}

func TestKubectlStyleHelp(t *testing.T) {
	root := "kubectl controls the Kubernetes cluster manager.\n\nBasic Commands (Beginner):\n  create          Create a resource\n\nDeploy Commands:\n  rollout         Manage the rollout\n\nUsage:\n  kubectl [flags] [options]\n\nUse \"kubectl options\" for a list of global command-line options (applies to all commands).\n"
	subs, _ := parseHelp(root)
	if len(subs) != 2 || subs[1].name != "rollout" {
		t.Fatalf("subcommands = %+v", subs)
	}
	get := "Display resources.\n\nOptions:\n    -A, --all-namespaces=false:\n\tIf present, list across namespaces.\n\n    --field-selector='':\n\tSelector.\n\n    --chunk-size=500:\n\tChunks.\n"
	_, flags := parseHelp(get)
	types := valueTypes(get)
	if len(flags) != 4 || types["--all-namespaces"] != "" || types["--field-selector"] != "string" || types["--chunk-size"] != "int" {
		t.Fatalf("flags %v types %v", flags, types)
	}
	options := "The following options can be passed to any command:\n\n    --as='':\n\tUsername to impersonate.\n\n    -n, --namespace='':\n\tNamespace.\n"
	if got := flagNames(options); strings.Join(got, " ") != "--as -n --namespace" {
		t.Fatalf("options page flags = %v", got)
	}
	if !globalOptionsRE.MatchString(root) {
		t.Fatal("global options hint not recognised")
	}
}

func TestAliases(t *testing.T) {
	if got := aliases("Show pods.\n\nAliases:\npod, pods, po\n\nExamples:\n  x\n"); strings.Join(got, " ") != "pod pods po" {
		t.Fatalf("kubectl aliases = %v", got)
	}
	if got := aliases("Usage:\n  tool list [flags]\n\nAliases:\n  list, ls\n"); strings.Join(got, " ") != "list ls" {
		t.Fatalf("cobra aliases = %v", got)
	}
	cli := CLI{Commands: []Command{{Path: ""}, {Path: "top"}, {Path: "top pod", Aliases: []string{"pods", "po"}}}}
	if path, ok := cli.Child("top", "pods"); !ok || path != "top pod" {
		t.Fatalf("Child(top, pods) = %q, %v", path, ok)
	}
	if _, ok := cli.Child("", "pods"); ok {
		t.Fatal("aliases must only match under their own parent")
	}
}

func TestWrappedProgram(t *testing.T) {
	if got := wrappedProgram("kubectl controls the cluster.\n\nUsage:\n  kubectl [flags] [options]\n", "devlab", []string{"kubectl"}); got != "kubectl" {
		t.Fatalf("kubectl = %q", got)
	}
	if got := wrappedProgram("Usage: devlab bootstrap [OPTIONS]\n", "devlab", []string{"bootstrap"}); got != "" {
		t.Fatalf("own command = %q", got)
	}
	cli := CLI{Commands: []Command{{Path: ""}, {Path: "kubectl", Wraps: "kubectl"}, {Path: "kubectl get", Wraps: "kubectl"}, {Path: "bootstrap"}}}
	if cli.WrappedBy("kubectl get") != "kubectl" || cli.WrappedBy("kubectl") != "kubectl" || cli.WrappedBy("bootstrap") != "" {
		t.Fatal("WrappedBy")
	}
}

func TestTrackedFilesInDefaultExcludes(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		full := filepath.Join(root, path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("bin/deploy.sh", "#!/bin/bash\n# Deploys.\nlog() { echo \"$1\"; }\n")
	write("bin/skipped.sh", "#!/bin/bash\nskip() { :; }\n")
	write("build/out.sh", "#!/bin/bash\nbuilt() { :; }\n")
	write("docgen.yaml", "exclude:\n  - bin/skipped.sh\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "bin", "docgen.yaml"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, output)
		}
	}
	cfg, _ := config.Load(root)
	f, err := Build(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, file := range f.Files {
		got[file.Path] = true
	}
	// Tracked under bin/ stays; the repository's own exclude and untracked build output go.
	if !got["bin/deploy.sh"] || got["bin/skipped.sh"] || got["build/out.sh"] {
		t.Fatalf("files = %v", got)
	}
}

func TestActionFlags(t *testing.T) {
	help := "Usage:\n  clctl tasks [flags]\n  clctl tasks [command]\n\nAvailable Commands:\n  list        Tree of open tasks\n  update      Change a task\n\nFlags:\n  -l, --list     Tree of open tasks\n  -u, --update   Change a task\n      --limit int   at most this many\n  -h, --help     help for tasks\n"
	command, _ := commandFromHelp(help, "clctl", []string{"tasks"}, "")
	want := map[string]string{"-l": "tasks list", "--list": "tasks list", "-u": "tasks update", "--update": "tasks update"}
	if len(command.Actions) != len(want) {
		t.Fatalf("actions = %v", command.Actions)
	}
	for flag, path := range want {
		if command.Actions[flag] != path {
			t.Fatalf("actions = %v", command.Actions)
		}
	}
}

// TestProbeFindsUnlistedCommands asks a cobra-style tool about a hidden command, a
// deprecated name, a hidden group's subcommand and a word that is not a command.
func TestProbeFindsUnlistedCommands(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
case "$*" in
"--help") printf 'A tool\n\nUsage:\n  tool [command]\n\nAvailable Commands:\n  sub         A command\n\nFlags:\n  -h, --help   help for tool\n\nUse "tool [command] --help" for more information about a command.\n' ;;
"sub --help") printf 'Usage:\n  tool sub [flags]\n\nFlags:\n  -h, --help   help for sub\n' ;;
"inspect --help") printf 'Usage:\n  tool inspect [flags]\n\nFlags:\n      --since string   how far back\n  -h, --help   help for inspect\n' ;;
"old-sub --help") printf 'Command "old-sub" is deprecated, use '"'"'tool sub'"'"'\nUsage:\n  tool old-sub [flags]\n\nFlags:\n  -h, --help   help\n' ;;
"cache --help") printf 'Usage:\n  tool cache [command]\n\nAvailable Commands:\n  refresh     Refresh\n\nUse "tool cache [command] --help" for more information about a command.\n' ;;
"cache refresh --help") printf 'Usage:\n  tool cache refresh [flags]\n\nFlags:\n      --quiet   say nothing\n' ;;
*) printf 'Error: unknown command\nUsage:\n  tool [command]\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "tool"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := config.CLI{Name: "tool", Run: []string{"./tool"}, Dir: dir}
	cli, err := ScrapeCLI(dir, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cli.Probe("inspect"); ok {
		t.Fatal("a tree without a prober answered a probe")
	}
	cli.prober = newProber(dir, settings)
	if command, ok := cli.Probe("inspect"); !ok || command.Path != "inspect" || !contains(command.ValueFlags, "--since") {
		t.Fatalf("inspect: %+v, %v", command, ok)
	}
	if command, ok := cli.Probe("old-sub"); !ok || command.Path != "old-sub" {
		t.Fatalf("old-sub: %+v, %v", command, ok)
	}
	if command, ok := cli.Probe("cache refresh"); !ok || !contains(command.Flags, "--quiet") {
		t.Fatalf("cache refresh: %+v, %v", command, ok)
	}
	for _, path := range []string{"bogus", "sub bogus"} {
		if command, ok := cli.Probe(path); ok {
			t.Fatalf("%s is not a command, but the probe found %+v", path, command)
		}
	}
	cache, _ := cli.Probe("cache")
	cli.Commands = append(cli.Commands, cache)
	if !cli.ListsSubcommands("cache") || cli.ListsSubcommands("sub") {
		t.Fatal("ListsSubcommands")
	}
}

func TestInScope(t *testing.T) {
	cases := []struct {
		path, scope string
		want        bool
	}{
		{"cmd/root.go", "cmd", true},
		{"cmd/root.go", "cmd/", true},
		{"cmd/root.go", "./cmd", true},
		{"cmdx/root.go", "cmd", false},
		{"main.go", "cmd", false},
		{"main.go", ".", true},
		{"cmd/root.go", ".", true},
		{"cmd/root.go", "", true},
	}
	for _, c := range cases {
		if got := InScope(c.path, c.scope); got != c.want {
			t.Errorf("InScope(%q, %q) = %v, want %v", c.path, c.scope, got, c.want)
		}
	}
}

// A CLI at the repository root (dir: .) has a hash: the whole repository, docs aside.
func TestFileHashRepositoryRoot(t *testing.T) {
	f := &Facts{
		Files: []File{{Path: "main.go", Hash: "a"}, {Path: "cmd/root.go", Hash: "b"}, {Path: "README.md", Hash: "c"}},
	}
	root := f.FileHash(".")
	if root == "" {
		t.Fatal(`FileHash(".") is empty`)
	}
	f.Files[2].Hash = "changed"
	if f.FileHash(".") != root {
		t.Error(`FileHash(".") changed with a Markdown file`)
	}
	f.Files[1].Hash = "changed"
	if f.FileHash(".") == root {
		t.Error(`FileHash(".") did not change with a Go file`)
	}
}
