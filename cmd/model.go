package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/llm"
)

// modelSession starts the model server when a command needs it and stops it again if
// this run started it, also on Ctrl+C. One place for what draft, code and ask share.
type modelSession struct {
	cfg     *config.Config
	Client  *llm.Client
	started bool
	ready   bool
	stopped chan struct{}
}

func newModelSession(cfg *config.Config) *modelSession {
	client := llm.New(cfg.LLM.URL, cfg.LLM.Model)
	client.Progress = progressLine()
	session := &modelSession{cfg: cfg, Client: client, stopped: make(chan struct{})}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-interrupts:
			session.Stop()
			os.Exit(130)
		case <-session.stopped:
			signal.Stop(interrupts)
		}
	}()
	return session
}

// Ensure makes sure the server answers, starting it with llm.start when configured.
func (s *modelSession) Ensure() error {
	if s.ready {
		return nil
	}
	err := s.Client.Healthy()
	if err != nil && s.cfg.LLM.Start != "" && s.cfg.LLM.Start != "none" {
		fmt.Fprintf(os.Stderr, "Starting the model server (%s)...\n", s.cfg.LLM.Start)
		start := exec.Command("sh", "-c", s.cfg.LLM.Start)
		start.Stdout, start.Stderr = os.Stderr, os.Stderr
		if startErr := start.Run(); startErr != nil {
			return fmt.Errorf("starting the model server: %w", startErr)
		}
		s.started = true
		err = s.Client.Healthy()
	}
	s.ready = err == nil
	return err
}

// Stop stops the server if this run started it. Safe to call more than once.
func (s *modelSession) Stop() {
	if s.started && s.cfg.LLM.Stop != "" {
		fmt.Fprintln(os.Stderr, "Stopping the model server.")
		if err := exec.Command("sh", "-c", s.cfg.LLM.Stop).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: stopping model server: %v\n", err)
		}
		s.started = false
	}
	select {
	case <-s.stopped:
	default:
		close(s.stopped)
	}
}

// CheckBudget fails before the model is asked when the prompt plus the answer cannot fit
// the server's context (known for llama-server; unknown servers are not checked), so an
// oversized request is an error here, never a silently cut prompt.
func (s *modelSession) CheckBudget(prompt string, responseToks int) error {
	promptToks := llm.EstimateTokens(prompt)
	context := s.Client.ContextSize()
	if context > 0 && promptToks+responseToks > context {
		return fmt.Errorf("the prompt is about %d tokens and the answer may take %d, but the server's context is %d; narrow the scope or lower llm.max_source_toks/max_response_toks in %s",
			promptToks, responseToks, context, config.FileName)
	}
	fmt.Fprintf(os.Stderr, "Prompt: about %d tokens", promptToks)
	if context > 0 {
		fmt.Fprintf(os.Stderr, " of %d context", context)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}
