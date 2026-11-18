package draft

import (
	"strings"
	"testing"

	"github.com/JB0t/docgen/internal/facts"
)

func TestUpsertAndHash(t *testing.T) {
	text := "# notes\n\nHand-written intro.\n\n## Usage\n\nkeep me\n"
	text = upsert(text, "m:overview", "m", "h1", "Overview text.", true)
	text = upsert(text, "m:reference", "m", "h1", "## Reference\n- x", false)
	if !strings.HasPrefix(text, "# notes\n\n<!-- docgen:begin id=m:overview scope=m hash=h1 -->\nOverview text.\n<!-- docgen:end id=m:overview -->\n\nHand-written intro.") {
		t.Fatalf("overview not placed under the title:\n%s", text)
	}
	if !strings.Contains(text, "keep me") || !strings.HasSuffix(text, "<!-- docgen:end id=m:reference -->\n") {
		t.Fatalf("reference not appended or human text lost:\n%s", text)
	}
	updated := upsert(text, "m:overview", "m", "h2", "New overview.", true)
	if strings.Count(updated, "docgen:begin id=m:overview") != 1 || !strings.Contains(updated, "New overview.") || sectionHash(updated, "m:overview") != "h2" {
		t.Fatalf("replace failed:\n%s", updated)
	}
	if strings.Contains(remove(updated, "m:overview"), "New overview.") {
		t.Fatal("remove failed")
	}
}

func TestHelpers(t *testing.T) {
	if got := escapeHTML("returns <root>/x and `<kept>`"); got != "returns &lt;root>/x and `<kept>`" {
		t.Fatalf("escapeHTML = %q", got)
	}
	for path, want := range map[string]bool{"a/b_test.go": true, "tests/x.py": true, "a/test_x.py": true, "a/b.go": false, "src/Api.Tests/A.cs": true} {
		if isTest(path) != want {
			t.Errorf("isTest(%q) = %v", path, !want)
		}
	}
	if got := cleanAnswer("```markdown\n# Title\nBody.\n```"); got != "Body." {
		t.Fatalf("cleanAnswer = %q", got)
	}
}

func TestUsageHelpers(t *testing.T) {
	cli := facts.CLI{Name: "tool", Commands: []facts.Command{
		{Path: "", Flags: []string{"-h", "--help", "--config"}},
		{Path: "tasks", Short: "Work with tasks", Flags: []string{"--config"}, Help: "Usage:\n  tool tasks [flags]\n  tool tasks [command]"},
		{Path: "tasks list", Short: "List <open> tasks | fast", Flags: []string{"--everyone", "--config", "-h"}},
		{Path: "cache", Short: "Cache group", Help: "Usage:\n  tool cache [command]"},
		{Path: "cache refresh", Short: "Refresh"},
	}}
	table := commandTable(cli)
	for _, want := range []string{"| `tool tasks list` | List &lt;open> tasks \\| fast | `--everyone` |", "Global flags: `--config`"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	if hasOwnAction(cli, cli.Commands[3]) || !hasOwnAction(cli, cli.Commands[1]) || !hasOwnAction(cli, cli.Commands[4]) {
		t.Error("hasOwnAction: groups without their own action must be skipped, leaves and runnable groups kept")
	}
	blocks := splitBlocks("### `tool tasks list`\nLists.\n```bash\ntool tasks list\n```\n\n### tool  cache refresh\nRefreshes.")
	if len(blocks) != 2 || blocks["tool cache refresh"] != "### `tool cache refresh`\n\nRefreshes." {
		t.Fatalf("splitBlocks = %q", blocks)
	}
}

func TestUpsertBeforeReference(t *testing.T) {
	text := upsert("# m\n", "m:reference", "m", "h", "## Reference", false)
	text = upsertBefore(text, "cli:tool:usage", "m", "h", "## Usage", "reference")
	if strings.Index(text, "## Usage") > strings.Index(text, "## Reference") {
		t.Fatalf("usage not placed before the reference:\n%s", text)
	}
}

func TestExampleHelpers(t *testing.T) {
	help := "Stop your timer\n\nUsage:\n  tool stop [flags]\n\nFlags:\n  -h, --help   help\n\nGlobal Flags:\n      --config string   config\n      --refresh   refresh\n"
	if got := stripGlobalFlags(help); strings.Contains(got, "--config") || !strings.Contains(got, "-h, --help") {
		t.Fatalf("stripGlobalFlags = %q", got)
	}
	cli := facts.CLI{Name: "tool"}
	command := facts.Command{Path: "tasks stop", Short: "Stop your timer"}
	if got := fallbackBlock(cli, command); got != "### `tool tasks stop`\n\nStop your timer.\n\n```bash\ntool tasks stop\n```" {
		t.Fatalf("fallbackBlock = %q", got)
	}
}

func TestDedupeCommands(t *testing.T) {
	block := "### `tool cache path`\n\nShow it.\n\n```bash\n# Show the path\ntool cache path\n\n# Again\ntool cache path\n```"
	if got := dedupeCommands(block); got != "### `tool cache path`\n\nShow it.\n\n```bash\n# Show the path\ntool cache path\n```" {
		t.Fatalf("dedupeCommands = %q", got)
	}
}

func TestCurrent(t *testing.T) {
	both := upsert(upsert("# m\n", "m:overview", "m", "h1", "Overview.", true), "m:reference", "m", "h1", "## Reference", false)
	onlyOverview := upsert("# m\n", "m:overview", "m", "h1", "Overview.", true)
	onlyReference := upsert("# m\n", "m:reference", "m", "h1", "## Reference", false)
	cases := []struct {
		name      string
		text      string
		reference bool
		want      bool
	}{
		{"both, references on", both, true, true},
		{"both, references off", both, false, true},
		{"overview only, references off", onlyOverview, false, true},
		{"overview only, references on", onlyOverview, true, false},
		{"reference kept after a failed overview", onlyReference, true, true},
		{"no generated sections", "# m\n\nHand-written.\n", false, false},
	}
	for _, c := range cases {
		if got := current(c.text, "m", "h1", c.reference); got != c.want {
			t.Errorf("%s: current = %v, want %v", c.name, got, c.want)
		}
	}
	if current(both, "m", "h2", false) {
		t.Error("changed sources reported current")
	}
}

func TestTableRows(t *testing.T) {
	if n := tableRows(facts.CLI{Name: "x", Commands: []facts.Command{{Path: ""}, {Path: "completion bash"}}}); n != 0 {
		t.Fatalf("root and completion only: %d rows", n)
	}
	if n := tableRows(facts.CLI{Name: "x", Commands: []facts.Command{{Path: ""}, {Path: "run"}}}); n != 1 {
		t.Fatalf("one command: %d rows", n)
	}
}
