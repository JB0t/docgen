package code

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JB0t/docgen/internal/config"
)

const sampleAnswer = "Here is the change.\n\n" +
	"pkg/main.go\n<<<<<<< SEARCH\nfunc old() int {\n\treturn 1\n}\n=======\nfunc renamed() int {\n\treturn 2\n}\n>>>>>>> REPLACE\n\n" +
	"`pkg/new.go`\n<<<<<<< SEARCH\n=======\npackage pkg\n\nvar created = true\n>>>>>>> REPLACE\n"

func TestParseBlocks(t *testing.T) {
	blocks, err := ParseBlocks(sampleAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].Path != "pkg/main.go" || blocks[1].Path != "pkg/new.go" {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0].Search != "func old() int {\n\treturn 1\n}\n" || blocks[0].Replace != "func renamed() int {\n\treturn 2\n}\n" {
		t.Fatalf("block 0 = %+v", blocks[0])
	}
	if blocks[1].Search != "" || !strings.Contains(blocks[1].Replace, "var created") {
		t.Fatalf("block 1 = %+v", blocks[1])
	}
	if _, err := ParseBlocks("no blocks here"); err == nil {
		t.Fatal("expected an error without blocks")
	}
	if _, err := ParseBlocks("<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE\n"); err == nil || !strings.Contains(err.Error(), "no file path") {
		t.Fatalf("missing path: %v", err)
	}
}

func TestApplyBlocksProducesAppliablePatch(t *testing.T) {
	root := gitRepo(t, map[string]string{
		"pkg/main.go": "package pkg\n\nfunc old() int {\n\treturn 1\n}\n",
	})
	blocks, err := ParseBlocks(sampleAnswer)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := ApplyBlocks(root, blocks)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"diff --git a/pkg/main.go b/pkg/main.go", "--- a/pkg/main.go", "+++ b/pkg/main.go",
		"diff --git a/pkg/new.go b/pkg/new.go", "new file mode", "--- /dev/null", "+++ b/pkg/new.go", "+func renamed() int {"} {
		if !strings.Contains(patch, want) {
			t.Fatalf("patch lacks %q:\n%s", want, patch)
		}
	}
	if err := ValidatePaths(patch, []string{"pkg"}, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	apply := exec.Command("git", "-C", root, "apply", "--check", "-")
	apply.Stdin = strings.NewReader(patch)
	if output, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("git apply --check: %s: %v", output, err)
	}
	apply = exec.Command("git", "-C", root, "apply", "-")
	apply.Stdin = strings.NewReader(patch)
	if output, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %s: %v", output, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "pkg/main.go"))
	if !strings.Contains(string(data), "func renamed() int {\n\treturn 2\n}") {
		t.Fatalf("main.go after apply:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "pkg/new.go")); err != nil {
		t.Fatal("new.go was not created")
	}
}

func TestApplyBlockTolerantMatching(t *testing.T) {
	content := "    if a {\n        b()\n    }\n"
	// Trailing whitespace in the file, none in the search.
	got, err := applyBlock("x \ny\n", Block{Search: "x\ny\n", Replace: "z\n"})
	if err != nil || got != "z\n" {
		t.Fatalf("trailing whitespace: %q, %v", got, err)
	}
	// Model lost the indentation entirely: match anyway and re-indent its replacement.
	got, err = applyBlock(content, Block{Search: "if a {\n    b()\n}\n", Replace: "if a {\n    c()\n}\n"})
	if err != nil || got != "    if a {\n        c()\n    }\n" {
		t.Fatalf("reindent: %q, %v", got, err)
	}
	if _, err := applyBlock(content, Block{Search: "nothing like this\n", Replace: "x\n"}); err == nil {
		t.Fatal("expected not-found error")
	}
	// Missing file with a non-empty search is an error, not a silent create.
	root := t.TempDir()
	if _, err := ApplyBlocks(root, []Block{{Path: "a.go", Search: "x\n", Replace: "y\n"}}); err == nil {
		t.Fatal("expected error for a missing file")
	}
	if _, err := ApplyBlocks(root, []Block{{Path: "../a.go", Search: "", Replace: "y\n"}}); err == nil {
		t.Fatal("expected error for a path outside the repo")
	}
}

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
	}
	return root
}

func TestApplyBlocksOverEditsEarlierContent(t *testing.T) {
	root := t.TempDir()
	first, order, exists, err := ApplyBlocksOver(root, nil, []Block{{Path: "play.yml", Replace: "---\n- hosts: all\n  tasks: []\n"}})
	if err != nil || len(order) != 1 || exists["play.yml"] {
		t.Fatalf("create: %v %v %v", order, exists, err)
	}
	second, order, _, err := ApplyBlocksOver(root, first, []Block{{Path: "play.yml", Search: "  tasks: []\n", Replace: "  tasks:\n    - ansible.builtin.ping:\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if second["play.yml"] != "---\n- hosts: all\n  tasks:\n    - ansible.builtin.ping:\n" || len(order) != 1 {
		t.Errorf("fix applied to the in-memory file: %q %v", second["play.yml"], order)
	}
	if first["play.yml"] != "---\n- hosts: all\n  tasks: []\n" {
		t.Error("the earlier contents must not change")
	}
	patch, err := DiffContents(root, second, order, map[string]bool{})
	if err != nil || !strings.Contains(patch, "+++ b/play.yml") || !strings.Contains(patch, "--- /dev/null") {
		t.Errorf("diff: %v\n%s", err, patch)
	}
}
