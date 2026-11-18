package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/JB0t/docgen/internal/check"
	codegen "github.com/JB0t/docgen/internal/code"
	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/facts"
	"github.com/JB0t/docgen/internal/fix"
	"github.com/JB0t/docgen/internal/llm"
)

// runFix has the model correct the hand-written claims check proved wrong, one doc at a
// time, through the code engine (search/replace blocks, path validation, git apply
// --check). Prints each patch, or applies it with apply. Returns how many docs were
// patched and how many still have findings the model could not fix.
func runFix(f *facts.Facts, cfg *config.Config, findings []check.Finding, apply bool) (patched, unfixed int, err error) {
	byDoc := map[string][]check.Finding{}
	var docs []string
	for _, finding := range findings {
		if _, seen := byDoc[finding.Doc]; !seen {
			docs = append(docs, finding.Doc)
		}
		byDoc[finding.Doc] = append(byDoc[finding.Doc], finding)
	}
	date := time.Now().Format("2006-01-02")
	session := newModelSession(cfg)
	defer session.Stop()
	for _, doc := range docs {
		text, readErr := os.ReadFile(f.Root + "/" + doc)
		if readErr != nil {
			return patched, unfixed, readErr
		}
		fixable := fix.Fixable(string(text), byDoc[doc])
		if len(fixable) == 0 {
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: %d finding(s) to fix\n", doc, len(fixable))
		if err := session.Ensure(); err != nil {
			return patched, unfixed, err
		}
		prompt, err := codegen.PromptFiles(f.Root, []string{doc}, fix.Request(doc, fixable, date), cfg.LLM.MaxSourceToks, codegen.FormatBlocks)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skipped: %v\n", err)
			unfixed++
			continue
		}
		if err := session.CheckBudget(prompt, cfg.LLM.MaxResponseToks); err != nil {
			return patched, unfixed, err
		}
		messages := []llm.Message{
			{Role: "system", Content: "You correct documentation so it matches the code. Change only what the findings require. Do not explain your answer."},
			{Role: "user", Content: prompt},
		}
		var patch string
		for attempt := 1; attempt <= 2; attempt++ {
			answer, stats, err := session.Client.Chat(messages, cfg.LLM.MaxResponseToks)
			if err != nil {
				return patched, unfixed, err
			}
			fmt.Fprintf(os.Stderr, "  model: %s\n", stats)
			problem := ""
			patch, problem = fixPatch(f, cfg, doc, answer, date)
			if problem == "" {
				break
			}
			if attempt == 2 {
				fmt.Fprintf(os.Stderr, "  gave up: %s\n", problem)
				patch = ""
				break
			}
			fmt.Fprintf(os.Stderr, "  rejected: %s; asking again\n", problem)
			messages = append(messages,
				llm.Message{Role: "assistant", Content: answer},
				llm.Message{Role: "user", Content: "That answer could not be applied:\n" + problem + "\n\nReturn the complete corrected answer in the same format, nothing else."})
		}
		if patch == "" {
			unfixed++
			continue
		}
		if apply {
			applyPatch := exec.Command("git", "-C", f.Root, "apply", "-")
			applyPatch.Stdin = strings.NewReader(patch)
			if output, err := applyPatch.CombinedOutput(); err != nil {
				return patched, unfixed, fmt.Errorf("applying patch for %s: %s: %w", doc, output, err)
			}
			fmt.Fprintf(os.Stderr, "  applied; every edited region starts with <!-- %s %s: ... -->\n", fix.Marker, date)
		}
		fmt.Print(patch)
		patched++
	}
	return patched, unfixed, nil
}

// fixPatch turns the model's answer for one doc into a checked patch, or says what is
// wrong with it. Besides the code engine's own checks, the result must not introduce
// new findings and must resolve the ones it was given.
func fixPatch(f *facts.Facts, cfg *config.Config, doc, answer, date string) (patch string, problem string) {
	blocks, err := codegen.ParseBlocks(answer)
	if err != nil {
		return "", err.Error()
	}
	for _, block := range blocks {
		if strings.TrimPrefix(strings.TrimPrefix(block.Path, "a/"), "b/") != doc {
			return "", fmt.Sprintf("a block changes %s, but only %s may change", block.Path, doc)
		}
	}
	blocks = fix.Mark(blocks, date)
	contents, _, _, err := codegen.ApplyBlocksContents(f.Root, blocks)
	if err != nil {
		return "", err.Error()
	}
	remaining := fix.Fixable(contents[doc], check.Text(f, cfg, doc, contents[doc]))
	if len(remaining) > 0 {
		var list []string
		for _, finding := range remaining {
			list = append(list, fmt.Sprintf("line %d: %s", finding.Line, finding.Message))
		}
		return "", "after the change these are still wrong:\n" + strings.Join(list, "\n")
	}
	patch, err = codegen.ApplyBlocks(f.Root, blocks)
	if err != nil {
		return "", err.Error()
	}
	if err := codegen.ValidatePaths(patch, []string{doc}, cfg); err != nil {
		return patch, err.Error()
	}
	checkApply := exec.Command("git", "-C", f.Root, "apply", "--check", "-")
	checkApply.Stdin = strings.NewReader(patch)
	if output, err := checkApply.CombinedOutput(); err != nil {
		return patch, "git apply --check failed: " + strings.TrimSpace(string(output))
	}
	return patch, ""
}
