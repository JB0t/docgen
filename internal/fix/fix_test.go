package fix

import (
	"strings"
	"testing"

	"github.com/JB0t/docgen/internal/check"
	"github.com/JB0t/docgen/internal/code"
)

func TestFixableSkipsGeneratedSectionsAndOtherKinds(t *testing.T) {
	text := "# Doc\n\nRun `tool old`.\n\n<!-- docgen:begin id=x:overview scope=x hash=1 -->\n`Missing` symbol\n<!-- docgen:end id=x:overview -->\n`Other` symbol\n"
	findings := []check.Finding{
		{Doc: "README.md", Line: 3, Severity: check.Error, Kind: "command", Message: "command tool old does not exist"},
		{Doc: "README.md", Line: 6, Severity: check.Warning, Kind: "symbol", Message: "inside generated"},
		{Doc: "README.md", Line: 8, Severity: check.Warning, Kind: "symbol", Message: "outside generated"},
		{Doc: "README.md", Line: 5, Severity: check.Warning, Kind: "stale", Message: "stale section"},
		{Doc: "README.md", Line: 0, Severity: check.Info, Kind: "coverage", Message: "no doc mentions X"},
	}
	kept := Fixable(text, findings)
	if len(kept) != 2 || kept[0].Line != 3 || kept[1].Line != 8 {
		t.Fatalf("Fixable = %+v", kept)
	}
}

func TestMarkAddsOrCleansMarkerLines(t *testing.T) {
	blocks := []code.Block{
		{Path: "README.md", Search: "a\n", Replace: "b\n"},
		{Path: "README.md", Search: "c\n", Replace: "<!-- docgen:edited 2026-10-09: replaced `old` with new -->\nd\n"},
		{Path: "README.md", Search: "e\n", Replace: ""},
	}
	marked := Mark(blocks, "2026-10-09")
	if !strings.HasPrefix(marked[0].Replace, "<!-- docgen:edited 2026-10-09: hand-written text changed") || !strings.HasSuffix(marked[0].Replace, "\nb\n") {
		t.Fatalf("block 0 = %q", marked[0].Replace)
	}
	if strings.Contains(marked[1].Replace, "`") || !strings.HasSuffix(marked[1].Replace, "\nd\n") {
		t.Fatalf("block 1 = %q", marked[1].Replace)
	}
	if Count(marked[0].Replace+marked[1].Replace+marked[2].Replace) != 3 {
		t.Fatalf("expected 3 markers")
	}
	if !strings.Contains(Request("README.md", []check.Finding{{Line: 3, Message: "m"}}, "2026-10-09"), "- line 3: m") {
		t.Fatal("request lacks the finding")
	}
}
