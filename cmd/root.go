// Package cmd defines docgen's commands.
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/docgen/internal/check"
	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/draft"
	"github.com/JB0t/docgen/internal/facts"
	"github.com/JB0t/docgen/internal/llm"
)

var version = "dev"

// Execute runs docgen and returns the exit code.
func Execute() int {
	if err := newRoot().Execute(); err != nil {
		if code, ok := err.(exitCode); ok {
			return int(code)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 2
	}
	return 0
}

// exitCode is returned by check to fail without printing an error.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "docgen",
		Short:         "Extract documentation facts from a repository and check docs against them",
		Version:       buildVersion(version),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newFactsCmd(), newCheckCmd(), newDraftCmd(), newCodeCmd(), newAskCmd(), newModulesCmd(), newSpecCmd(), newGenCmd(), newValidateCmd())
	root.PersistentFlags().BoolVar(&rescan, "rescan", false, "re-read every CLI's --help instead of using the cached command tree (kept 24h)")
	return root
}

// rescan is the global --rescan flag: re-read CLI help instead of using the cache.
var rescan bool

func load(repo string) (*facts.Facts, *config.Config, error) {
	cfg, err := config.Load(repo)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", config.FileName, err)
	}
	cfg.Rescan = rescan
	f, err := facts.Build(repo, cfg)
	return f, cfg, err
}

func newFactsCmd() *cobra.Command {
	var output string
	var summary bool
	cmd := &cobra.Command{
		Use:   "facts [REPO]",
		Short: "Extract facts (files, symbols, env vars, docs, CLI commands) as JSON",
		Long: `Scans the repository (default: current directory) without any AI and writes its
fact sheet: files and languages, declared symbols with signatures and doc comments,
environment variables read by the code, documentation files and their headings, and
the commands and flags of CLIs listed in docgen.yaml.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := "."
			if len(args) == 1 {
				repo = args[0]
			}
			f, _, err := load(repo)
			if err != nil {
				return err
			}
			if summary {
				printSummary(f)
				return nil
			}
			if output == "" || output == "-" {
				data, _ := json.MarshalIndent(f, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			return f.Write(output)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write JSON here instead of stdout")
	cmd.Flags().BoolVar(&summary, "summary", false, "print counts instead of JSON")
	return cmd
}

func printSummary(f *facts.Facts) {
	fmt.Printf("%s: %d files, %d symbols, %d env vars, %d docs, %d CLIs\n", f.Root, len(f.Files), len(f.Symbols), len(f.EnvVars), len(f.Docs), len(f.CLIs))
	var languages []string
	for language, count := range f.Languages {
		languages = append(languages, fmt.Sprintf("%s %d", language, count))
	}
	sort.Strings(languages)
	fmt.Println("languages:", strings.Join(languages, ", "))
	kinds := map[string]int{}
	documented := 0
	exported := 0
	for _, symbol := range f.Symbols {
		kinds[symbol.Language+" "+symbol.Kind]++
		if symbol.Exported {
			exported++
			if symbol.Doc != "" {
				documented++
			}
		}
	}
	var lines []string
	for kind, count := range kinds {
		lines = append(lines, fmt.Sprintf("%s %d", kind, count))
	}
	sort.Strings(lines)
	fmt.Println("symbols:", strings.Join(lines, ", "))
	if exported > 0 {
		fmt.Printf("doc comments on exported symbols: %d of %d (%d%%)\n", documented, exported, 100*documented/exported)
	}
	for _, cli := range f.CLIs {
		fmt.Printf("cli %s: %d commands\n", cli.Name, len(cli.Commands))
	}
}

func newCheckCmd() *cobra.Command {
	var asJSON, strict, coverage, doFix, apply bool
	cmd := &cobra.Command{
		Use:   "check [REPO]",
		Short: "Check docs against the code: commands, flags, paths, symbols, markers",
		Long: `Validates every doc against the facts. Errors: CLI subcommands or flags that do not
exist, repo paths that do not exist, broken or orphaned docgen markers. Warnings: code
identifiers, env vars or flags in backticks that appear nowhere in the code, and
generated sections whose sources changed. Exits 1 on errors (or warnings with
--strict), so it works in CI and git hooks.

--fix is the one way docgen edits text a person wrote: for each doc with wrong
commands, flags, paths, identifiers or env vars outside generated sections, the local
model corrects just those lines (search/replace blocks through the same checks as
'docgen code'), and every edited region starts with a
<!-- docgen:edited DATE: what changed --> comment, so the edit is loud in the file and
in git diff. The result is re-checked; a change that leaves a finding or adds one is
rejected and asked for once more. Patches are printed for review; --apply applies
them. Stale generated sections are not --fix's job: run 'docgen draft'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := "."
			if len(args) == 1 {
				repo = args[0]
			}
			f, cfg, err := load(repo)
			if err != nil {
				return err
			}
			findings := check.Run(f, cfg, check.Options{Coverage: coverage})
			counts := map[check.Severity]int{}
			for _, finding := range findings {
				counts[finding.Severity]++
			}
			if asJSON {
				data, _ := json.MarshalIndent(findings, "", "  ")
				fmt.Println(string(data))
			} else {
				for _, finding := range findings {
					fmt.Println(finding)
				}
				fmt.Printf("%d docs checked: %d errors, %d warnings", len(f.Docs), counts[check.Error], counts[check.Warning])
				if coverage {
					fmt.Printf(", %d coverage gaps", counts[check.Info])
				}
				fmt.Println()
			}
			if doFix {
				if asJSON {
					return fmt.Errorf("--fix prints patches; drop --json")
				}
				patched, unfixed, err := runFix(f, cfg, findings, apply)
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "--fix: %d doc(s) patched%s, %d left with findings the model could not fix.\n",
					patched, map[bool]string{true: " and applied", false: " (printed, not applied)"}[apply], unfixed)
				if unfixed > 0 {
					return exitCode(1)
				}
				return nil
			}
			if counts[check.Error] > 0 || strict && counts[check.Warning] > 0 {
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&doFix, "fix", false, "have the local model correct wrong claims in hand-written text (opt-in; marks every edit)")
	cmd.Flags().BoolVar(&apply, "apply", false, "with --fix: apply the patches instead of only printing them")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print findings as JSON")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail on warnings too")
	cmd.Flags().BoolVar(&coverage, "coverage", false, "also list commands and env vars no doc mentions")
	return cmd
}

func newDraftCmd() *cobra.Command {
	var docPath string
	var clis []string
	var force, dryRun, all, newDocs bool
	cmd := &cobra.Command{
		Use:   "draft [SCOPE...]",
		Short: "Write module overviews (local model), exact references and CLI usage into READMEs",
		Long: `For each SCOPE (a module directory), writes two sections between docgen markers in
SCOPE/README.md: an overview drafted by the local model from the module's facts and
source, and a reference list of its exported symbols built straight from the facts.
The overview is checked like any doc; one that mentions things the code does not
contain is retried once with the problems listed, then left out.

--cli NAME writes a usage section for a CLI from docgen.yaml: a command table built
from its --help output, and model-written examples that are each checked against the
command tree (failing examples are retried once, then dropped).

--all drafts every module under the SCOPE (default: the whole repo) and every
configured CLI there; see 'docgen modules'. Anything whose sources have not changed
since it was written is skipped. --all only starts generated sections in a module that
has none yet when docgen.yaml lists it under draft.new_docs (or with --new-docs);
elsewhere it keeps existing generated sections current. The reference list of exported
symbols is written only when docgen.yaml sets draft.reference.`,
		Example: `  llm-server start
  docgen draft internal/notes --dry-run
  docgen draft --cli mytool --doc USAGE.md
  docgen draft --all internal`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && len(clis) == 0 && !all {
				return fmt.Errorf("give a module directory, --cli NAME or --all")
			}
			f, cfg, err := load(".")
			if err != nil {
				return err
			}
			scopes := args
			if all {
				root := "."
				if len(args) > 0 {
					root = args[0]
				}
				scopes = nil
				unlisted := 0
				for _, module := range draft.Modules(f, root) {
					if module.State == "new" && !newDocs && !cfg.Draft.AllowsNewDoc(module.Scope) {
						unlisted++
						continue
					}
					scopes = append(scopes, module.Scope)
				}
				if unlisted > 0 {
					fmt.Fprintf(os.Stderr, "%d modules without generated docs left out (not under draft.new_docs; --new-docs drafts them).\n", unlisted)
				}
				for _, cli := range f.CLIs {
					if root == "." || cli.Dir == root || strings.HasPrefix(cli.Dir, strings.TrimSuffix(root, "/")+"/") {
						clis = append(clis, cli.Name)
					}
				}
				fmt.Fprintf(os.Stderr, "%d modules and %d CLIs to consider.\n", len(scopes), len(clis))
			}
			client := llm.New(cfg.LLM.URL, cfg.LLM.Model)
			client.Progress = progressLine()
			logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
			// The server is started only when a module actually needs the model, and
			// stopped again afterwards (also on Ctrl+C) if this run started it.
			serverReady, startedServer := false, false
			stopServer := func() {
				if startedServer && cfg.LLM.Stop != "" {
					logf("Stopping the model server.")
					_ = exec.Command("sh", "-c", cfg.LLM.Stop).Run()
					startedServer = false
				}
			}
			defer stopServer()
			interrupts := make(chan os.Signal, 1)
			signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(interrupts)
			go func() {
				if _, ok := <-interrupts; ok {
					stopServer()
					os.Exit(130)
				}
			}()
			ensureServer := func() error {
				if serverReady {
					return nil
				}
				err := client.Healthy()
				if err != nil && cfg.LLM.Start != "" && cfg.LLM.Start != "none" {
					logf("Starting the model server (%s)...", cfg.LLM.Start)
					start := exec.Command("sh", "-c", cfg.LLM.Start)
					start.Stdout, start.Stderr = os.Stderr, os.Stderr
					if startErr := start.Run(); startErr != nil {
						return fmt.Errorf("starting the model server: %w", startErr)
					}
					startedServer = true
					err = client.Healthy()
				}
				serverReady = err == nil
				return err
			}
			failed, written, skipped := 0, 0, 0
			report := func(name string, result draft.Result, err error) {
				switch {
				case err != nil:
					logf("  error: %v", err)
					failed++
				case result.Skipped != "":
					logf("  skipped: %s", result.Skipped)
					skipped++
				case dryRun:
					fmt.Println(result.Text)
				default:
					logf("  wrote %s to %s", strings.Join(result.Sections, ", "), result.Doc)
					written++
				}
				if len(result.Findings) > 0 {
					logf("  %d generated part(s) left out after failing checks", len(result.Findings))
				}
				if result.Fallbacks > 0 {
					logf("  %d command(s) shown without model examples", result.Fallbacks)
				}
			}
			options := func(scope string) draft.Options {
				return draft.Options{Scope: scope, Doc: docPath, Force: force, DryRun: dryRun, MaxSourceToks: cfg.LLM.MaxSourceToks, Reference: cfg.Draft.Reference, Log: logf}
			}
			started := time.Now()
			for _, scope := range scopes {
				logf("%s:", scope)
				if !force && draft.UpToDate(f, scope, docPath, cfg.Draft.Reference) {
					report(scope, draft.Result{Skipped: "up to date"}, nil)
					continue
				}
				if err := ensureServer(); err != nil {
					return err
				}
				result, err := draft.Module(f, cfg, client, options(scope))
				report(scope, result, err)
			}
			for _, name := range clis {
				var cli *facts.CLI
				for index := range f.CLIs {
					if f.CLIs[index].Name == name {
						cli = &f.CLIs[index]
					}
				}
				if cli == nil {
					return fmt.Errorf("no CLI %q in %s", name, config.FileName)
				}
				logf("%s (CLI usage):", name)
				if err := ensureServer(); err != nil {
					return err
				}
				result, err := draft.CLIUsage(f, cfg, client, *cli, options(cli.Dir))
				report(name, result, err)
			}
			logf("Done in %s: %d written, %d up to date, %d failed.", time.Since(started).Round(time.Second), written, skipped, failed)
			if failed > 0 {
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&docPath, "doc", "", "target Markdown file (default SCOPE/README.md, or the CLI's doc)")
	cmd.Flags().StringArrayVar(&clis, "cli", nil, "write the usage section for this CLI from docgen.yaml (repeatable)")
	cmd.Flags().BoolVar(&all, "all", false, "every module and CLI under SCOPE (default: the whole repo)")
	cmd.Flags().BoolVar(&force, "force", false, "regenerate even if the sources are unchanged")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the updated documents instead of writing them")
	cmd.Flags().BoolVar(&newDocs, "new-docs", false, "with --all: also start docs in modules not under draft.new_docs")
	return cmd
}

func newModulesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "modules [ROOT]",
		Short: "List the modules 'draft --all' would document, and their state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			f, cfg, err := load(".")
			if err != nil {
				return err
			}
			counts := map[string]int{}
			for _, module := range draft.Modules(f, root) {
				state := module.State
				if state == "new" && !cfg.Draft.AllowsNewDoc(module.Scope) {
					state = "not listed" // --all leaves it out (draft.new_docs)
				}
				fmt.Printf("%-11s %4d symbols  %s\n", state, module.Symbols, module.Scope)
				counts[state]++
			}
			fmt.Printf("%d new, %d not listed, %d stale, %d up to date\n", counts["new"], counts["not listed"], counts["stale"], counts["up to date"])
			return nil
		},
	}
}

// saveAnswer writes the model's latest answer (or the patch made from it) to
// <git dir>/docgen/<name> for inspection, overwriting the previous one, and returns the
// path, or a note when it could not be saved.
func saveAnswer(root, name, text string) string {
	gitDir, err := exec.Command("git", "-C", root, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return "not saved: " + err.Error()
	}
	dir := filepath.Join(strings.TrimSpace(string(gitDir)), "docgen")
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "not saved: " + err.Error()
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "not saved: " + err.Error()
	}
	return path
}

// progressLine returns a Progress callback that keeps one status line on stderr while
// the model works: prompt processing, then the answer's tokens and speed. It draws
// nothing when stderr is not a terminal, so logs stay clean.
func progressLine() func(llm.Progress) {
	if info, err := os.Stderr.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	var last time.Time
	var answerStarted time.Duration
	return func(progress llm.Progress) {
		if progress.Done {
			fmt.Fprint(os.Stderr, "\r\033[K")
			last, answerStarted = time.Time{}, 0
			return
		}
		if progress.Tokens == 1 {
			answerStarted = progress.Elapsed
		}
		if time.Since(last) < 250*time.Millisecond {
			return
		}
		last = time.Now()
		var status string
		switch {
		case progress.Tokens > 0:
			speed := ""
			if generating := (progress.Elapsed - answerStarted).Seconds(); generating > 0.5 {
				speed = fmt.Sprintf(", %.1f tok/s", float64(progress.Tokens)/generating)
			}
			lines := strings.Count(progress.Text, "\n")
			status = fmt.Sprintf("writing: %d tokens, %d lines%s", progress.Tokens, lines, speed)
		case progress.PromptTotal > 0:
			status = fmt.Sprintf("reading prompt: %d/%d tokens (%d%%)", progress.PromptDone, progress.PromptTotal,
				100*progress.PromptDone/progress.PromptTotal)
		default:
			status = "waiting for the model"
		}
		fmt.Fprintf(os.Stderr, "\r\033[K  model %s  [%s]", status, progress.Elapsed.Round(time.Second))
	}
}
