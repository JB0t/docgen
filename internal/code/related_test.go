package code

import (
	"strings"
	"testing"

	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/facts"
)

func TestTermsSplitsIdentifiersAndDropsFiller(t *testing.T) {
	terms := Terms("Move 'clctl tasks list-timers' to a new listTimers subcommand")
	joined := " " + strings.Join(terms, " ") + " "
	for _, want := range []string{"clctl", "tasks", "list", "timers", "listtimers", "subcommand"} {
		if !strings.Contains(joined, " "+want+" ") {
			t.Fatalf("terms %v lack %q", terms, want)
		}
	}
	for _, unwanted := range []string{"move", "new", "to", "a"} {
		if strings.Contains(joined, " "+unwanted+" ") {
			t.Fatalf("terms %v contain filler %q", terms, unwanted)
		}
	}
}

func TestRelatedRanksByPathSymbolsAndContent(t *testing.T) {
	root := gitRepo(t, map[string]string{
		"cmd/timers.go":    "package cmd\n\n// ListTimers prints running timers.\nfunc ListTimers() {}\n",
		"cmd/notes.go":     "package cmd\n\nfunc Notes() {}\n",
		"cmd/tasks.go":     "package cmd\n\n// tasks mention timers in a comment\nfunc Tasks() {}\n",
		"pkg/unrelated.go": "package pkg\n\nfunc Other() {}\n",
	})
	cfg := &config.Config{}
	f, err := facts.Build(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	paths, terms, err := Related(root, f, cfg, "Move list-timers into its own timers command", 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) == 0 {
		t.Fatal("no terms")
	}
	joined := strings.Join(paths, ",")
	if !strings.Contains(joined, "cmd/timers.go") || !strings.Contains(joined, "cmd/tasks.go") {
		t.Fatalf("related = %v", paths)
	}
	if strings.Contains(joined, "unrelated") || strings.Contains(joined, "notes") {
		t.Fatalf("related includes unrelated files: %v", paths)
	}
	// A budget too small for any file picks nothing rather than overflowing.
	paths, _, err = Related(root, f, cfg, "timers", 5)
	if err != nil || len(paths) != 0 {
		t.Fatalf("tiny budget = %v, %v", paths, err)
	}
}
