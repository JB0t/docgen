package draft

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JB0t/docgen/internal/check"
	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/facts"
	"github.com/JB0t/docgen/internal/llm"
)

const examplesSystem = `You write usage examples for a command-line tool's documentation.

For each command you are given (with its full --help output), write:
### ` + "`<tool> <command>`" + `
One caveman-style line on when to use it (terse, no articles or filler, e.g. "Rebuild cache before offline work.").
` + "```bash" + `
1 to 3 realistic invocations, each preceded by a short # comment
` + "```" + `

Rules:
- Use only commands and flags that appear in the given help text, spelled exactly.
- Use realistic placeholder values (e.g. TECH-1234, 2026-09-21) rather than <angle brackets>.
- Show what a user would actually run; if a command has no flags, one invocation is enough.
- Quote values that contain spaces.
- Keep the commands in the order given. No other text.`

// commandsPerPrompt keeps each examples prompt small enough to stay fast on CPU.
const commandsPerPrompt = 6

// CLIUsage writes a usage section for a configured CLI: an exact command table built
// from its scraped help, and model-written examples that are each checked against the
// command tree (failing examples are retried once, then dropped).
func CLIUsage(f *facts.Facts, cfg *config.Config, client *llm.Client, cli facts.CLI, options Options) (Result, error) {
	doc := cli.Doc
	if doc == "" {
		doc = filepath.ToSlash(filepath.Join(cli.Dir, "README.md"))
	}
	if options.Doc != "" {
		doc = options.Doc
	}
	result := Result{Doc: doc}
	id := "cli:" + cli.Name + ":usage"
	hash := cliHash(f, cli)
	existing, _ := os.ReadFile(filepath.Join(f.Root, doc))
	text := string(existing)
	if text == "" {
		text = "# " + cli.Name + "\n"
	}
	if !options.Force && sectionHash(text, id) == hash {
		result.Skipped = "up to date"
		return result, nil
	}

	var commands []facts.Command
	for _, command := range cli.Commands {
		if insideWrapped(cli, command.Path) {
			continue // the wrapped tool's own commands are its documentation, not ours
		}
		if command.Path != "" && !strings.HasPrefix(command.Path, "completion ") && (hasOwnAction(cli, command) || command.Wraps != "") {
			commands = append(commands, command)
		}
	}
	if tableRows(cli) == 0 {
		// An empty table helps nobody: the --help scrape found no commands (see run, dir
		// and env in docgen.yaml).
		result.Skipped = "no commands found in its --help"
		return result, nil
	}
	var examples []string
	for start := 0; start < len(commands); start += commandsPerPrompt {
		batch := commands[start:min(start+commandsPerPrompt, len(commands))]
		blocks, fallbacks, err := exampleBatch(f, cfg, client, cli, batch, doc, options)
		if err != nil {
			return result, err
		}
		examples = append(examples, blocks...)
		result.Fallbacks += fallbacks
	}

	var body strings.Builder
	body.WriteString("## Usage\n\n")
	body.WriteString(commandTable(cli))
	if len(examples) > 0 {
		body.WriteString("\n### Examples\n\n")
		body.WriteString(strings.Join(examples, "\n\n"))
	}
	text = upsertBefore(text, id, cli.Dir, hash, body.String(), "reference")
	result.Sections = []string{id}
	result.Text = text
	if options.DryRun {
		return result, nil
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(f.Root, doc)), 0o755); err != nil {
		return result, err
	}
	return result, os.WriteFile(filepath.Join(f.Root, doc), []byte(text), 0o644)
}

// cliHash is the hash of the CLI's source directory, the same value the checker uses
// to tell whether the section is stale (its help output comes from that source).
func cliHash(f *facts.Facts, cli facts.CLI) string { return f.FileHash(cli.Dir) }

// insideWrapped reports whether path is below a wrapper command (`devlab kubectl get`
// under `devlab kubectl`); the wrapper itself is not inside.
func insideWrapped(cli facts.CLI, path string) bool {
	wrapper := cli.WrappedBy(path)
	return wrapper != "" && wrapper != path
}

// hasOwnAction skips pure command groups whose help only lists subcommands.
func hasOwnAction(cli facts.CLI, command facts.Command) bool {
	if !hasChildren(cli, command.Path) {
		return true
	}
	// Groups that also run something themselves say so in their usage line.
	return strings.Contains(command.Help, "Usage:\n  "+cli.Name+" "+command.Path+" [flags]")
}

func hasChildren(cli facts.CLI, path string) bool {
	for _, command := range cli.Commands {
		if command.Path != path && strings.HasPrefix(command.Path, path+" ") {
			return true
		}
	}
	return false
}

// tableRows counts the commands commandTable lists.
func tableRows(cli facts.CLI) int {
	rows := 0
	for _, command := range cli.Commands {
		if command.Path != "" && !strings.HasPrefix(command.Path, "completion ") && !insideWrapped(cli, command.Path) {
			rows++
		}
	}
	return rows
}

// commandTable lists every command with its description and flags.
func commandTable(cli facts.CLI) string {
	var builder strings.Builder
	builder.WriteString("| Command | Description | Flags |\n|---|---|---|\n")
	for _, command := range cli.Commands {
		if command.Path == "" || strings.HasPrefix(command.Path, "completion ") || insideWrapped(cli, command.Path) {
			continue
		}
		if command.Wraps != "" {
			fmt.Fprintf(&builder, "| `%s %s ...` | Runs `%s` with the given arguments (see `%s --help`) | |\n", cli.Name, command.Path, command.Wraps, command.Wraps)
			continue
		}
		var flags []string
		for _, flag := range command.Flags {
			if flag != "-h" && flag != "--help" && !inheritedFlag(cli, command.Path, flag) {
				flags = append(flags, "`"+flag+"`")
			}
		}
		fmt.Fprintf(&builder, "| `%s %s` | %s | %s |\n", cli.Name, command.Path,
			strings.ReplaceAll(escapeHTML(command.Short), "|", "\\|"), strings.Join(flags, " "))
	}
	if root, ok := cli.Find(""); ok {
		var global []string
		for _, flag := range root.Flags {
			if flag != "-h" && flag != "--help" {
				global = append(global, "`"+flag+"`")
			}
		}
		if len(global) > 0 {
			fmt.Fprintf(&builder, "\nGlobal flags: %s\n", strings.Join(global, " "))
		}
	}
	return builder.String()
}

// inheritedFlag reports whether the root command already defines the flag (global flags
// are listed once, not on every row).
func inheritedFlag(cli facts.CLI, path, flag string) bool {
	root, ok := cli.Find("")
	if !ok || path == "" {
		return false
	}
	for _, known := range root.Flags {
		if known == flag {
			return true
		}
	}
	return false
}

// exampleHeadingRE accepts "### `tool cmd`" and "### tool cmd" (models often drop the
// backticks); headings are normalised to the backticked form.
var exampleHeadingRE = regexp.MustCompile("(?m)^#{2,4}\\s+`?([^`\\n]+?)`?\\s*$")

// stripGlobalFlags drops the "Global Flags" section from help text, so examples do not
// pad themselves with flags that every command accepts.
func stripGlobalFlags(help string) string {
	var kept []string
	skipping := false
	for _, line := range strings.Split(help, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "Global Flags:") {
			skipping = true
			continue
		}
		if skipping && trimmed != "" && !strings.HasPrefix(line, " ") {
			skipping = false
		}
		if !skipping {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// hint turns checker findings into a concrete instruction for the retry.
func hint(cli facts.CLI, command facts.Command, findings []check.Finding) string {
	var hints []string
	for _, finding := range findings {
		switch {
		case strings.Contains(finding.Message, "takes no positional arguments"):
			hints = append(hints, "This command takes no positional arguments: pass every value with a flag (for example a task ID with --task-id), and quote values that contain spaces.")
		case finding.Kind == "flag":
			var own []string
			for _, flag := range command.Flags {
				if flag != "-h" && flag != "--help" {
					own = append(own, flag)
				}
			}
			hints = append(hints, "Use only these flags: "+strings.Join(own, " ")+".")
		case finding.Kind == "value":
			hints = append(hints, finding.Message+".")
		case finding.Kind == "command":
			hints = append(hints, fmt.Sprintf("Write the command exactly as `%s %s`, followed only by its flags.", cli.Name, command.Path))
		default:
			hints = append(hints, finding.Message+".")
		}
	}
	return strings.Join(unique(hints), "\n")
}

func unique(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

// fallbackBlock is used when the model cannot produce valid examples: the bare command
// and its description, which is always correct.
func fallbackBlock(cli facts.CLI, command facts.Command) string {
	description := strings.TrimSpace(command.Short)
	if description != "" && !strings.HasSuffix(description, ".") {
		description += "."
	}
	return fmt.Sprintf("### `%s %s`\n\n%s\n\n```bash\n%s %s\n```", cli.Name, command.Path, description, cli.Name, command.Path)
}

// exampleBatch asks for examples for a few commands at once, retries any command whose
// examples fail the checks on its own with a targeted hint (twice), and falls back to
// the bare command so every command keeps a correct entry. It returns the blocks in
// command order and how many commands fell back.
func exampleBatch(f *facts.Facts, cfg *config.Config, client *llm.Client, cli facts.CLI, batch []facts.Command, doc string, options Options) ([]string, int, error) {
	prompt := func(commands []facts.Command) string {
		var builder strings.Builder
		fmt.Fprintf(&builder, "TOOL: %s\n", cli.Name)
		for _, command := range commands {
			fmt.Fprintf(&builder, "\nCOMMAND: %s %s\n%s\n", cli.Name, command.Path, stripGlobalFlags(command.Help))
		}
		builder.WriteString("\nWrite the examples.")
		return builder.String()
	}
	accepted := map[string]string{}
	failures := map[string][]check.Finding{}
	accept := func(answer string, wanted map[string]bool) {
		blocks := splitBlocks(cleanAnswer(answer))
		// A single-command answer often comes without its heading: take it whole.
		if len(wanted) == 1 && len(blocks) == 0 && strings.Contains(answer, "```") {
			for heading := range wanted {
				blocks[heading] = "### `" + heading + "`\n\n" + cleanAnswer(answer)
			}
		}
		for heading, block := range blocks {
			if !wanted[heading] || accepted[heading] != "" {
				continue
			}
			block = dedupeCommands(block)
			if findings := blockProblems(check.Text(f, cfg, doc, block)); len(findings) > 0 {
				failures[heading] = findings
				continue
			}
			accepted[heading] = block
			delete(failures, heading)
		}
	}

	wanted := map[string]bool{}
	for _, command := range batch {
		wanted[cli.Name+" "+command.Path] = true
	}
	answer, stats, err := client.Chat([]llm.Message{{Role: "system", Content: examplesSystem}, {Role: "user", Content: prompt(batch)}}, 2500)
	if err != nil {
		return nil, 0, err
	}
	options.Log("  examples %s…: %s", batch[0].Path, stats)
	accept(answer, wanted)

	// Retry each command that failed or was skipped, alone and with a concrete hint.
	for _, command := range batch {
		heading := cli.Name + " " + command.Path
		for attempt := 1; attempt <= 2 && accepted[heading] == ""; attempt++ {
			messages := []llm.Message{{Role: "system", Content: examplesSystem}, {Role: "user", Content: prompt([]facts.Command{command}) +
				"\nStart with the line: ### `" + heading + "`"}}
			if findings := failures[heading]; len(findings) > 0 {
				messages[1].Content += "\n\nA previous attempt was wrong:\n" + hint(cli, command, findings)
			}
			answer, stats, err := client.Chat(messages, 800)
			if err != nil {
				return nil, 0, err
			}
			options.Log("  retry %s (%d): %s", command.Path, attempt, stats)
			accept(answer, map[string]bool{heading: true})
			if accepted[heading] == "" {
				reason := "no usable example in the answer"
				if findings := failures[heading]; len(findings) > 0 {
					reason = findings[0].Message
				}
				options.Log("    rejected: %s", reason)
			}
		}
	}

	var blocks []string
	fallbacks := 0
	for _, command := range batch {
		block := accepted[cli.Name+" "+command.Path]
		if block == "" {
			block = fallbackBlock(cli, command)
			fallbacks++
			options.Log("  %s: no valid examples after retries; using the bare command", command.Path)
		}
		blocks = append(blocks, block)
	}
	return blocks, fallbacks, nil
}

// dedupeCommands drops repeated command lines inside an example block, together with
// the comment line directly above a dropped repeat.
func dedupeCommands(block string) string {
	lines := strings.Split(block, "\n")
	seen := map[string]bool{}
	var kept []string
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			kept = append(kept, line)
			continue
		}
		if inFence && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			if seen[trimmed] {
				if len(kept) > 0 && strings.HasPrefix(strings.TrimSpace(kept[len(kept)-1]), "#") {
					kept = kept[:len(kept)-1]
				}
				// Also drop the blank line separating it from the previous example.
				for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
					kept = kept[:len(kept)-1]
				}
				continue
			}
			seen[trimmed] = true
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// splitBlocks maps "tool command" headings to their full Markdown block.
func splitBlocks(answer string) map[string]string {
	blocks := map[string]string{}
	matches := exampleHeadingRE.FindAllStringSubmatchIndex(answer, -1)
	for index, match := range matches {
		end := len(answer)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		heading := strings.Join(strings.Fields(answer[match[2]:match[3]]), " ")
		body := strings.TrimSpace(answer[match[1]:end])
		blocks[heading] = "### `" + heading + "`\n\n" + body
	}
	return blocks
}

// blockProblems keeps findings that mean an example uses something that does not exist.
func blockProblems(findings []check.Finding) []check.Finding {
	var kept []check.Finding
	for _, finding := range findings {
		if finding.Severity == check.Error || finding.Kind == "flag" {
			kept = append(kept, finding)
		}
	}
	return kept
}
