package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/gen"
	"github.com/JB0t/docgen/internal/llm"
	"github.com/JB0t/docgen/internal/specs"
)

func newGenCmd() *cobra.Command {
	var request string
	var out, contextPaths, examples, specNames, setNames []string
	var maxSpecs, retries int
	var plan, showPrompt, apply, force, scaffold bool
	cmd := &cobra.Command{
		Use:   "gen KIND --request TEXT [--out PATH]... [--context PATH]... [--example PATH]...",
		Short: "Generate new files (boilerplate) with the local model, from retrieved specs, checked before writing",
		Long: `Writes the first version of new files for a request: a playbook or role, a workflow or
pipeline, a Terraform module, a Helm chart. KIND is one of: ` + gen.KindNames() + `.

The prompt holds, in this order: the reference specs the request needs (retrieved from
'docgen spec' by keywords and meaning, plus any --spec), --example files whose
conventions to follow, --context files the request builds on (read only; the values
they need from a caller are listed for the model, and a workflow that does not pass
them fails its check), the current files under --out, and the request.

The answer (search/replace blocks) is applied in memory and checked in a throwaway
copy of the tree: syntax; modules, resources, actions and their options against the
specs; and the kind's own tools when installed (ansible-playbook --syntax-check,
ansible-lint, actionlint, terraform fmt/validate, helm lint/template, plus gen.validate
in docgen.yaml). Failures go back to the model for fix blocks, up to gen.retries times.

Nothing is written without --apply, and --apply refuses while checks fail unless
--force. --plan shows what the prompt would hold without asking the model.`,
		Example: `  docgen gen ansible --out playbooks/aks_nodepools.yml \
      --request "Provision AKS node pools in an existing cluster in a resource group"
  docgen gen ansible --scaffold --out roles/aks_nodepool --request @request.md --apply
  docgen gen github-actions --context playbooks/aks_nodepools.yml --out .github/workflows/aks-nodepools.yml \
      --request "workflow_dispatch workflow that takes the playbook's variables as inputs and runs it"
  docgen gen terraform --out infra/aks --spec azurerm_kubernetes_cluster_node_pool --request "..." --plan`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := gen.FindKind(args[0])
			if err != nil {
				return err
			}
			request, err = readRequest(request)
			if err != nil {
				return err
			}
			root, cfg, err := genConfig()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			targets, err := rootRelative(root, cwd, out)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				targets, _ = rootRelative(root, cwd, []string{"."})
			}
			if scaffold {
				if err := runScaffold(kind, root, targets); err != nil {
					return err
				}
			}
			sets, err := specs.Load(specs.Dir())
			if err != nil {
				return err
			}
			if maxSpecs < 0 {
				maxSpecs = cfg.Gen.MaxSpecs
			}
			if retries < 0 {
				retries = max(cfg.Gen.Retries, 0)
			}
			planned, err := buildPlan(root, cwd, cfg, kind, sets, request, targets, contextPaths, examples, specNames, setNames, maxSpecs)
			if err != nil {
				return err
			}
			fmt.Fprint(os.Stderr, planned.Describe())
			if plan {
				if showPrompt {
					fmt.Println("=== system ===\n" + planned.System + "\n=== prompt ===\n" + planned.Prompt)
				}
				return nil
			}
			session := newModelSession(cfg)
			defer session.Stop()
			if err := session.Ensure(); err != nil {
				return err
			}
			if err := session.CheckBudget(planned.System+planned.Prompt, cfg.LLM.MaxResponseToks); err != nil {
				return err
			}
			logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
			result, err := gen.Generate(session.Client, gen.Session{
				Root: root, Plan: planned, Sets: sets, Extra: cfg.Gen.Validate[kind.Name],
				Retries: retries, MaxResponse: cfg.LLM.MaxResponseToks, Log: logf,
				Budget: func(messages []llm.Message) error {
					var all strings.Builder
					for _, message := range messages {
						all.WriteString(message.Content)
					}
					return session.CheckBudget(all.String(), cfg.LLM.MaxResponseToks)
				},
				Save: func(name, text string) string { return saveGen(root, name, text) },
			})
			if err != nil {
				return err
			}
			saved := saveGen(root, "gen.diff", result.Diff)
			printReport(result.Report)
			failing := len(result.Report.Problems) > 0
			if !apply {
				fmt.Print(result.Diff)
				fmt.Fprintf(os.Stderr, "Not written (diff saved to %s). Rerun with --apply, or apply the diff with git apply.\n", saved)
				if failing {
					return exitCode(1)
				}
				return nil
			}
			if failing && !force {
				fmt.Fprintf(os.Stderr, "Not written: checks still fail. Review the diff (%s) and apply it yourself, or rerun with --force.\n", saved)
				return exitCode(1)
			}
			for _, rel := range result.Order {
				target := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(target, []byte(result.Contents[rel]), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "Wrote %s\n", rel)
			}
			fmt.Fprintln(os.Stderr, "Review the files, then finish them: TODO comments mark what the model could not settle.")
			if failing {
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&request, "request", "", "what to generate (@FILE reads it from a file)")
	cmd.Flags().StringArrayVar(&out, "out", nil, "file or directory the model may create or change (repeatable; default: the current directory)")
	cmd.Flags().StringArrayVar(&contextPaths, "context", nil, "file or directory the request builds on, read only (repeatable)")
	cmd.Flags().StringArrayVar(&examples, "example", nil, "file whose conventions to follow (repeatable; adds to gen.examples)")
	cmd.Flags().StringArrayVar(&specNames, "spec", nil, "spec entry or set to include whatever the search finds (repeatable)")
	cmd.Flags().StringArrayVar(&setNames, "set", nil, "search only these spec sets (repeatable)")
	cmd.Flags().IntVar(&maxSpecs, "max-specs", -1, "specs picked by search (0: only --spec; default gen.max_specs)")
	cmd.Flags().IntVar(&retries, "retries", -1, "fix rounds after failed checks (default gen.retries)")
	cmd.Flags().BoolVar(&plan, "plan", false, "show what the prompt would hold, without asking the model")
	cmd.Flags().BoolVar(&showPrompt, "prompt", false, "with --plan: print the full prompt")
	cmd.Flags().BoolVar(&apply, "apply", false, "write the files when the checks pass")
	cmd.Flags().BoolVar(&force, "force", false, "with --apply: write even when checks still fail")
	cmd.Flags().BoolVar(&scaffold, "scaffold", false, "create the --out skeleton first with the kind's generator (ansible-galaxy role init, helm create) when it does not exist")
	return cmd
}

func readRequest(request string) (string, error) {
	if strings.HasPrefix(request, "@") {
		data, err := os.ReadFile(strings.TrimPrefix(request, "@"))
		if err != nil {
			return "", fmt.Errorf("reading the request: %w", err)
		}
		request = string(data)
	}
	if strings.TrimSpace(request) == "" {
		return "", fmt.Errorf("--request is required")
	}
	return request, nil
}

// rootRelative turns paths given relative to the current directory into paths relative
// to the root, refusing any outside it.
func rootRelative(root, cwd string, paths []string) ([]string, error) {
	var out []string
	for _, given := range paths {
		full := given
		if !filepath.IsAbs(full) {
			full = filepath.Join(cwd, given)
		}
		rel, err := filepath.Rel(root, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s is outside %s", given, root)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

func runScaffold(kind *gen.Kind, root string, targets []string) error {
	if kind.Scaffold == nil {
		return fmt.Errorf("kind %s has no scaffold generator; create the files yourself or let gen write them", kind.Name)
	}
	target := targets[0]
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(target))); err == nil {
		fmt.Fprintf(os.Stderr, "%s exists; not scaffolding.\n", target)
		return nil
	}
	command := kind.Scaffold(target)
	if command == nil {
		return fmt.Errorf("cannot scaffold %s as %s (an Ansible role goes in .../roles/NAME)", target, kind.Name)
	}
	fmt.Fprintf(os.Stderr, "Scaffolding: %s\n", strings.Join(command, " "))
	run := exec.Command(command[0], command[1:]...)
	run.Dir = root
	run.Stdout, run.Stderr = os.Stderr, os.Stderr
	if err := run.Run(); err != nil {
		return fmt.Errorf("scaffolding %s: %w", target, err)
	}
	return nil
}

func buildPlan(root, cwd string, cfg *config.Config, kind *gen.Kind, sets []*specs.Set, request string, targets, contextPaths, examples, specNames, setNames []string, maxSpecs int) (*gen.Plan, error) {
	plan := &gen.Plan{Kind: kind, Request: request, Out: targets}
	const maxFile = 64 << 10
	var err error
	var skipped []string
	if plan.Context, skipped, err = gen.ReadFiles(cwd, contextPaths, maxFile); err != nil {
		return nil, err
	}
	plan.Omitted = append(plan.Omitted, skipped...)
	plan.Inputs = gen.ContextInputs(plan.Context)
	allExamples := append(append([]string(nil), cfg.Gen.Examples[kind.Name]...), examples...)
	// gen.examples paths are relative to the root; --example to the current directory.
	plan.Examples = map[string]string{}
	for index, example := range allExamples {
		base := cwd
		if index < len(cfg.Gen.Examples[kind.Name]) {
			base = root
		}
		files, skipped, err := gen.ReadFiles(base, []string{example}, maxFile)
		if err != nil {
			return nil, err
		}
		for path, content := range files {
			plan.Examples[path] = content
		}
		plan.Omitted = append(plan.Omitted, skipped...)
	}
	plan.Targets = map[string]string{}
	for _, target := range targets {
		full := filepath.Join(root, filepath.FromSlash(target))
		if _, err := os.Stat(full); err != nil {
			if filepath.Ext(target) != "" {
				plan.Create = append(plan.Create, target)
			}
			continue
		}
		if target == "." {
			continue // the whole tree: existing files are not shown
		}
		files, skipped, err := gen.ReadFiles(root, []string{target}, maxFile)
		if err != nil {
			return nil, err
		}
		for path, content := range files {
			plan.Targets[path] = content
		}
		plan.Omitted = append(plan.Omitted, skipped...)
	}
	var explicit []specs.Hit
	for _, name := range specNames {
		hits, err := specs.Lookup(sets, name)
		if err != nil {
			return nil, err
		}
		explicit = append(explicit, hits...)
	}
	var ranked []specs.Hit
	if maxSpecs > 0 && len(sets) > 0 {
		filter := specs.Filter{Kinds: kind.SpecKinds, For: kind.Name, Sets: setNames}
		var note string
		ranked, note, err = searchSpecs(cfg, sets, filter, request, 3*maxSpecs)
		if err != nil {
			return nil, err
		}
		if note != "" {
			fmt.Fprintln(os.Stderr, note)
		}
		ranked = relevant(ranked)
	}
	plan.Specs, plan.Commons = gen.PickSpecs(explicit, ranked, maxSpecs, cfg.Gen.SpecToks)
	plan.Build()
	// Leave room for the answer: drop the lowest-ranked automatic specs while the prompt
	// would not fit max_source_toks.
	for plan.Tokens() > cfg.LLM.MaxSourceToks {
		last := -1
		for index, spec := range plan.Specs {
			if !spec.Explicit {
				last = index
			}
		}
		if last < 0 {
			break
		}
		plan.Omitted = append(plan.Omitted, "spec "+plan.Specs[last].Entry.Name)
		plan.Specs = append(plan.Specs[:last], plan.Specs[last+1:]...)
		plan.Build()
	}
	return plan, nil
}

// relevant keeps hits close to the best one: retrieval returns something for any
// request, and weak matches only cost context. Embedding similarities bunch together
// (nomic-embed-text puts unrelated modules at 0.6 and the right one at 0.7), so with
// vectors the margin is absolute; by keywords alone it is half the best score.
func relevant(hits []specs.Hit) []specs.Hit {
	if len(hits) == 0 {
		return nil
	}
	floor := hits[0].Score - 0.10
	if hits[0].Meaning == 0 {
		floor = hits[0].Score * 0.5
	}
	var kept []specs.Hit
	for _, hit := range hits {
		if hit.Score >= floor {
			kept = append(kept, hit)
		}
	}
	return kept
}

func printReport(report gen.Report) {
	if len(report.Ran) > 0 {
		fmt.Fprintf(os.Stderr, "Checks: %s\n", strings.Join(report.Ran, ", "))
	}
	for _, skipped := range report.Skipped {
		fmt.Fprintf(os.Stderr, "Skipped: %s\n", skipped)
	}
	if len(report.Advice) > 0 {
		fmt.Fprintf(os.Stderr, "Advice (%d, not blocking):\n", len(report.Advice))
		for _, problem := range report.Advice {
			fmt.Fprintf(os.Stderr, "  %s\n", strings.ReplaceAll(problem.String(), "\n", "\n    "))
		}
	}
	if len(report.Problems) > 0 {
		fmt.Fprintf(os.Stderr, "Failing (%d):\n", len(report.Problems))
		for _, problem := range report.Problems {
			fmt.Fprintf(os.Stderr, "  %s\n", strings.ReplaceAll(problem.String(), "\n", "\n    "))
		}
	} else {
		fmt.Fprintln(os.Stderr, "All checks passed.")
	}
}

// saveGen keeps generation output in .git/docgen/ inside a repository, else in
// $XDG_STATE_HOME/docgen/ (~/.local/state/docgen/).
func saveGen(root, name, text string) string {
	if saved := saveAnswer(root, name, text); !strings.HasPrefix(saved, "not saved") {
		return saved
	}
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	dir = filepath.Join(dir, "docgen")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "not saved: " + err.Error()
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "not saved: " + err.Error()
	}
	return path
}

func newValidateCmd() *cobra.Command {
	var contextPaths []string
	cmd := &cobra.Command{
		Use:   "validate KIND PATH...",
		Short: "Run gen's checks on files as they are (no model): specs, syntax and the kind's tools",
		Long: `Checks finished or hand-edited files the way docgen gen checks generated ones: syntax,
modules, resources, actions and options against the stored specs, the kind's tools
when installed, and gen.validate commands. --context files are read for the values a
workflow or pipeline must pass. Exits 1 when a check fails.`,
		Example: `  docgen validate ansible playbooks/aks_nodepools.yml
  docgen validate github-actions .github/workflows/aks-nodepools.yml --context playbooks/aks_nodepools.yml`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := gen.FindKind(args[0])
			if err != nil {
				return err
			}
			root, cfg, err := genConfig()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			targets, err := rootRelative(root, cwd, args[1:])
			if err != nil {
				return err
			}
			files, _, err := gen.ReadFiles(root, targets, 1<<20)
			if err != nil {
				return err
			}
			context, _, err := gen.ReadFiles(cwd, contextPaths, 1<<20)
			if err != nil {
				return err
			}
			sets, err := specs.Load(specs.Dir())
			if err != nil {
				return err
			}
			report, err := gen.Validate(root, kind, files, sets, gen.ContextInputs(context), cfg.Gen.Validate[kind.Name])
			if err != nil {
				return err
			}
			printReport(report)
			if len(report.Problems) > 0 {
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&contextPaths, "context", nil, "files the checked ones must pass values to (repeatable)")
	return cmd
}
