package check

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/facts"
)

func TestCheckFindsProblems(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		full := filepath.Join(root, path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(text), 0o644)
	}
	write("src/app.go", "package app\n\n// Run runs.\nfunc Run() { _ = os.Getenv(\"APP_MODE\") }\n")
	write("README.md", strings.Join([]string{
		"Call `Run()` or `Missing()`; set `APP_MODE`, not `APP_NOPE`.",
		"Source is in `src/app.go`, not `src/gone.go`; see `app.go`, `nope.yaml`, `alpine/kubectl`, `cr.l5d.io/linkerd/cli-bin`, `busybox:1.36`, `Blue/green`, `./missing.sh`.",
		"`tool sub one | two | nope` and `tool sub one | less`",
		"```bash",
		"tool sub one --fast",
		"tool sub one --slow",
		"tool bogus",
		"tool --config x.yaml sub one --fast",
		"tool --config x.yaml tool sub one",
		"tool sub two ARG",
		"tool sub uno --fast -- sh -c 'x'",
		"./tool sub one --slow",
		"tool sub one --task-id 2026-09-21 --limit five",
		"tool sub one --task-id TECH-12 --limit 5 --date yesterday",
		"```",
		"<!-- docgen:begin id=a scope=src hash=000000 -->",
		"<!-- docgen:end id=a -->",
	}, "\n"))
	cfg, _ := config.Load(root)
	f, err := facts.Build(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.CLIs = []facts.CLI{{Name: "tool", Commands: []facts.Command{
		{Path: "", Flags: []string{"--help", "--config"}, ValueFlags: []string{"--config"}}, {Path: "sub"}, {Path: "sub one", Aliases: []string{"uno"}, Flags: []string{"--fast", "--task-id", "--limit", "--date"}, ValueFlags: []string{"--task-id", "--limit", "--date"}, ValueTypes: map[string]string{"--task-id": "string", "--limit": "int", "--date": "string"}}, {Path: "sub two", NoArgs: true},
	}}}
	var got []string
	for _, finding := range Run(f, cfg, Options{}) {
		got = append(got, string(finding.Severity)+" "+finding.Kind+" "+finding.Ref)
	}
	want := []string{
		"warning symbol Missing",
		"warning env APP_NOPE",
		"error path src/gone.go",
		"warning path nope.yaml",
		"error path ./missing.sh",
		"error command tool sub nope",
		"error flag --slow",
		"error command tool bogus",
		"error command tool tool",
		"error command tool sub two",
		"error flag --slow",
		"error path ./tool",
		"error value --task-id 2026-09-21",
		"error value --limit five",
		"warning stale a",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestCheckAcceptsWhatExistsOutsideTheRepo covers references that are right although
// no committed file or help page shows them.
func TestCheckAcceptsWhatExistsOutsideTheRepo(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		full := filepath.Join(root, path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(text), 0o644)
	}
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	write(".gitignore", "conf/local.json\nsecrets\n")
	write("conf/app.yaml", "a: 1\n")
	write("server.sh", "#!/bin/bash\nprefix=SRV_FAST_\noverride=\"${prefix}THREADS\"\n")
	write("app.go", "package app\n\nfunc out() string { return filepath.Join(dir, \"status.md\") }\n")
	write("README.md", strings.Join([]string{
		"Set `SRV_FAST_THREADS`, not `SRV_SLOW_LIMIT`. Values go in `.tfvars` files.",
		"Answers land in `.git/tool/answer.txt`; each run writes `status.md`, never `report.md`.",
		"Local settings: `conf/local.json` and `conf/secrets/token.json`, not `conf/gone.json`.",
		"```bash",
		"tool t -l --limit 5",
		"tool t -l --nope",
		"tool t -x",
		"```",
	}, "\n"))
	cfg, _ := config.Load(root)
	f, err := facts.Build(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.CLIs = []facts.CLI{{Name: "tool", Commands: []facts.Command{
		{Path: "", Flags: []string{"--help"}},
		{Path: "tasks", Aliases: []string{"t"}, Flags: []string{"-l", "--list", "-x"}, Actions: map[string]string{"-l": "tasks list", "--list": "tasks list"}},
		{Path: "tasks list", Flags: []string{"--limit"}, ValueFlags: []string{"--limit"}, NoArgs: true},
	}}}
	var got []string
	for _, finding := range Run(f, cfg, Options{}) {
		got = append(got, string(finding.Severity)+" "+finding.Kind+" "+finding.Ref)
	}
	want := []string{
		"warning env SRV_SLOW_LIMIT",
		"warning path report.md",
		"error path conf/gone.json",
		"error flag --nope",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
