package code

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JB0t/docgen/internal/config"
)

func TestPromptUsesScopedFilesAndHonorsLimit(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"pkg/main.go":   "package pkg\n",
		"other/main.go": "package other\n",
		"pkg/.env":      "SECRET=value\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	prompt, err := Prompt(root, []string{"pkg"}, "change it", 1000, cfg, FormatDiff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `<file path="pkg/main.go">`) || strings.Contains(prompt, "other/main.go") || strings.Contains(prompt, "SECRET") {
		t.Fatalf("unexpected prompt content:\n%s", prompt)
	}
	if _, err := Prompt(root, []string{"pkg"}, "change it", 10, cfg, FormatDiff); err == nil {
		t.Fatal("expected context limit error")
	}
}

func TestCleanPatch(t *testing.T) {
	want := "diff --git a/pkg/main.go b/pkg/main.go\n--- a/pkg/main.go\n+++ b/pkg/main.go\n"
	for _, answer := range []string{want, "```diff\n" + want + "```"} {
		got, err := CleanPatch(answer)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("CleanPatch() = %q, want %q", got, want)
		}
	}
	for _, answer := range []string{"Here is your patch", "```diff\nunclosed"} {
		if _, err := CleanPatch(answer); err == nil {
			t.Fatalf("CleanPatch(%q) should fail", answer)
		}
	}
}

func TestValidatePaths(t *testing.T) {
	cfg := &config.Config{}
	patch := "diff --git a/pkg/main.go b/pkg/main.go\n"
	if err := ValidatePaths(patch, []string{"pkg"}, cfg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, patch string
		scopes      []string
	}{
		{"outside-scope", "diff --git a/other/main.go b/other/main.go\n", []string{"pkg"}},
		{"traversal", "diff --git a/../outside.go b/../outside.go\n", []string{"."}},
		{"quoted", "diff --git a/\"odd name.go\" b/\"odd name.go\"\n", []string{"."}},
		{"no-files", "not a diff\n", []string{"."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePaths(tc.patch, tc.scopes, cfg); err == nil {
				t.Fatal("expected patch validation error")
			}
		})
	}
}

func TestCleanPatchKeepsBlankContextLine(t *testing.T) {
	answer := "```diff\ndiff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n-x\n+y\n \n```\n"
	got, err := CleanPatch(answer)
	if err != nil || !strings.HasSuffix(got, "+y\n \n") {
		t.Fatalf("CleanPatch = %q, %v", got, err)
	}
}

func TestRepairPatch(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
		"@@ -1,9 +1,9 @@ func a\n one\n\n-two\n+TWO\n+three\n" + // wrong counts, blank line without its space
		"\ndiff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -3 +3 @@\n-x\n+y\n" +
		"This change renames x to y.\n" // trailing explanation
	got, fixes := RepairPatch(patch)
	want := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
		"@@ -1,3 +1,4 @@ func a\n one\n \n-two\n+TWO\n+three\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -3 +3 @@\n-x\n+y\n"
	if got != want {
		t.Fatalf("RepairPatch =\n%s\nwant\n%s", got, want)
	}
	if len(fixes) != 3 {
		t.Fatalf("fixes = %v", fixes)
	}
	if again, fixes := RepairPatch(want); again != want || len(fixes) != 0 {
		t.Fatalf("a valid patch changed: %q %v", again, fixes)
	}
}

func TestRepairedPatchApplies(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(stdin string, args ...string) error {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		command.Stdin = strings.NewReader(stdin)
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, output)
		}
		return nil
	}
	if err := run("", "init", "-q"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc A() {\n\treturn\n}\n"), 0o644)
	// What docgen used to pass git: the header promises a last blank context line that
	// was trimmed away (or that the model never wrote).
	broken := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,6 +1,6 @@\n package a\n\n-func A() {\n+func B() {\n \treturn\n }\n"
	if err := run(broken, "apply", "--check", "-"); err == nil || !strings.Contains(err.Error(), "corrupt patch") {
		t.Fatalf("expected git to call the unrepaired patch corrupt, got %v", err)
	}
	repaired, _ := RepairPatch(broken)
	if err := run(repaired, "apply", "-"); err != nil {
		t.Fatalf("repaired patch does not apply: %v\n%s", err, repaired)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.go"))
	if !strings.Contains(string(data), "func B()") {
		t.Fatalf("a.go = %q", data)
	}
}
