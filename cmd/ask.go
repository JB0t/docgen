package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JB0t/docgen/internal/ask"
)

func newAskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ask QUESTION [SCOPE...]",
		Short: "Answer a question about the code with the local model, checked against the facts",
		Long: `Asks the configured model a question about the repository. The prompt holds the
facts for the files concerned (exported symbols with their doc comments), the overviews
'docgen draft' already wrote for their modules, and as much of their source as
llm.max_source_toks allows. Without a SCOPE the files are picked from the question's
words, the same way 'docgen code' picks them.

The answer is checked like a document: identifiers, paths, commands or flags the code
does not contain are sent back to the model once. Anything still wrong is listed under
the answer, so a wrong claim is visible rather than silently trusted. The more modules
have overviews (docgen draft --all), the better the answers.`,
		Example: `  docgen ask "How does clctl decide which timers are running?"
  docgen ask "What does the digest command cache, and where?" cmd/digest.go`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			question, scopes := args[0], args[1:]
			if strings.TrimSpace(question) == "" {
				return fmt.Errorf("the question is empty")
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
			session := newModelSession(cfg)
			defer session.Stop()
			if err := session.Ensure(); err != nil {
				return err
			}
			logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
			result, err := ask.Answer(f, cfg, session.Client, ask.Options{Question: question, Scopes: scopes, MaxToks: cfg.LLM.MaxSourceToks, Log: logf})
			if err != nil {
				return err
			}
			if len(result.Terms) > 0 {
				logf("Files (picked for %s): %s", strings.Join(result.Terms, ", "), strings.Join(result.Files, " "))
			}
			logf("Model: %s", result.Stats)
			fmt.Println(result.Answer)
			if len(result.Findings) > 0 {
				fmt.Println()
				fmt.Println("Not found in the code (treat these parts of the answer as unverified):")
				for _, finding := range result.Findings {
					fmt.Printf("- %s\n", finding.Message)
				}
			}
			return nil
		},
	}
	return cmd
}
