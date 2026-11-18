package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	codegen "github.com/JB0t/docgen/internal/code"
	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/llm"
)

func newCodeCmd() *cobra.Command {
	var request, format string
	var apply bool
	cmd := &cobra.Command{
		Use:   "code --request CHANGE [SCOPE...]",
		Short: "Generate a scoped code patch with the local model",
		Long: `Reads source files under SCOPE, asks the configured model for the change, and prints
the resulting patch for review. --apply applies it only after git apply --check
succeeds. This command does not run generated code or tests.

Without a SCOPE, docgen picks the files the request most likely concerns (by path,
symbol names, doc comments and git grep) until llm.max_source_toks is spent, and says
which. Give files or directories to control the scope exactly.

--format diff (default) asks for a unified diff; --format blocks asks for search/replace
blocks, which small models get right more often, and docgen turns them into a diff. A
patch git rejects is sent back to the model once with git's message before giving up.`,
		Example: `  docgen code --request "Move timer commands from tasks to clctl timers list"
  docgen code --request "Add a --json flag" cmd/timers.go
  docgen code --format blocks --apply --request "Rename the command" cmd/timers.go`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, scopes []string) error {
			if strings.TrimSpace(request) == "" {
				return fmt.Errorf("--request is required")
			}
			if format != string(codegen.FormatDiff) && format != string(codegen.FormatBlocks) {
				return fmt.Errorf("--format must be diff or blocks")
			}
			repo, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
			if err != nil {
				return fmt.Errorf("find repository root: %w", err)
			}
			root := strings.TrimSpace(string(repo))
			f, cfg, err := load(root)
			if err != nil {
				return err
			}
			files := scopes
			if len(scopes) == 0 {
				picked, terms, err := codegen.Related(root, f, cfg, request, cfg.LLM.MaxSourceToks)
				if err != nil {
					return err
				}
				if len(picked) == 0 {
					return fmt.Errorf("no files match the request's words (%s); give the files or directories to change", strings.Join(terms, ", "))
				}
				fmt.Fprintf(os.Stderr, "Scope (picked for %s): %s\n", strings.Join(terms, ", "), strings.Join(picked, " "))
				files, scopes = picked, picked
			} else {
				files, err = codegen.SourceFiles(root, scopes, cfg)
				if err != nil {
					return err
				}
			}
			prompt, err := codegen.PromptFiles(root, files, request, cfg.LLM.MaxSourceToks, codegen.Format(format))
			if err != nil {
				return err
			}
			session := newModelSession(cfg)
			defer session.Stop()
			if err := session.Ensure(); err != nil {
				return err
			}
			if err := session.CheckBudget(prompt, cfg.LLM.MaxResponseToks); err != nil {
				return err
			}
			messages := []llm.Message{
				{Role: "system", Content: "You make precise code changes. Change only what the request requires. Do not explain your answer."},
				{Role: "user", Content: prompt},
			}
			var patch string
			for attempt := 1; attempt <= 2; attempt++ {
				answer, stats, err := session.Client.Chat(messages, cfg.LLM.MaxResponseToks)
				// Keep the raw answer, so a patch git rejects can be inspected.
				saved := saveAnswer(root, "answer.txt", answer)
				if err != nil {
					return fmt.Errorf("%w (answer so far: %s)", err, saved)
				}
				fmt.Fprintf(os.Stderr, "Model: %s\n", stats)
				var problem string
				patch, problem = patchFromAnswer(root, answer, codegen.Format(format), scopes, cfg)
				if problem == "" {
					break
				}
				savedPatch := ""
				if patch != "" {
					savedPatch = " (cleaned patch: " + saveAnswer(root, "patch.diff", patch) + ")"
				}
				if attempt == 2 {
					return fmt.Errorf("%s (raw answer: %s)%s", problem, saved, savedPatch)
				}
				fmt.Fprintf(os.Stderr, "Rejected: %s. Asking the model to correct it.\n", problem)
				messages = append(messages,
					llm.Message{Role: "assistant", Content: answer},
					llm.Message{Role: "user", Content: "That answer could not be applied:\n" + problem +
						"\n\nReturn the complete corrected answer in the same format, nothing else."})
			}
			saveAnswer(root, "patch.diff", patch)
			applyArgs := []string{"-C", root, "apply"}
			if needsLooseWhitespace(root, patch) {
				applyArgs = append(applyArgs, "--ignore-whitespace")
				fmt.Fprintln(os.Stderr, "The patch applies only when whitespace in its context lines is ignored (tabs written as spaces?); check the result with git diff and your formatter.")
			}
			if !apply {
				fmt.Print(patch)
				return nil
			}
			applyPatch := exec.Command("git", append(applyArgs, "-")...)
			applyPatch.Stdin = strings.NewReader(patch)
			if output, err := applyPatch.CombinedOutput(); err != nil {
				return fmt.Errorf("applying patch: %s: %w", output, err)
			}
			fmt.Println(patch)
			fmt.Fprintln(os.Stderr, "Applied patch. Review with git diff and run relevant tests.")
			return nil
		},
	}
	cmd.Flags().StringVar(&request, "request", "", "natural-language code change request")
	cmd.Flags().StringVar(&format, "format", string(codegen.FormatDiff), "answer format: diff (unified diff) or blocks (search/replace blocks)")
	cmd.Flags().BoolVar(&apply, "apply", false, "apply patch after git apply --check (default: print only)")
	return cmd
}

// patchFromAnswer turns a model answer into a patch that git accepts for the requested
// scopes, or returns the patch so far and what is wrong with it (for the retry prompt).
func patchFromAnswer(root, answer string, format codegen.Format, scopes []string, cfg *config.Config) (patch string, problem string) {
	var err error
	if format == codegen.FormatBlocks {
		blocks, err := codegen.ParseBlocks(answer)
		if err != nil {
			return "", err.Error()
		}
		patch, err = codegen.ApplyBlocks(root, blocks)
		if err != nil {
			return "", err.Error()
		}
	} else {
		patch, err = codegen.CleanPatch(answer)
		if err != nil {
			return "", err.Error()
		}
		var fixes []string
		patch, fixes = codegen.RepairPatch(patch)
		for _, fix := range fixes {
			fmt.Fprintf(os.Stderr, "Repaired the patch: %s.\n", fix)
		}
	}
	if err := codegen.ValidatePaths(patch, scopes, cfg); err != nil {
		return patch, err.Error()
	}
	check := exec.Command("git", "-C", root, "apply", "--check", "-")
	check.Stdin = strings.NewReader(patch)
	if output, err := check.CombinedOutput(); err != nil {
		loose := exec.Command("git", "-C", root, "apply", "--check", "--ignore-whitespace", "-")
		loose.Stdin = strings.NewReader(patch)
		if _, looseErr := loose.CombinedOutput(); looseErr != nil {
			return patch, "git apply --check failed: " + strings.TrimSpace(string(output))
		}
	}
	return patch, ""
}

// needsLooseWhitespace reports whether the patch applies only with --ignore-whitespace.
// Models often turn tabs into spaces in context lines; if only that stops the patch, it
// is applied that way with a warning.
func needsLooseWhitespace(root, patch string) bool {
	check := exec.Command("git", "-C", root, "apply", "--check", "-")
	check.Stdin = strings.NewReader(patch)
	return check.Run() != nil
}
