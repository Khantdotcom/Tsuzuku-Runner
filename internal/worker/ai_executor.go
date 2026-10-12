package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"text/template"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

// AIExecutorOptions configures the CLI-backed AI executor.
type AIExecutorOptions struct {
	// CommandTemplate is a Go text/template expanded with Repository, Revision,
	// Command, Prompt, and Acceptance (joined criteria). Example:
	// `agent run --repo {{.Repository}} --prompt {{printf "%q" .Prompt}}`
	CommandTemplate string
	// Enabled must be true or AI workloads fail at run time.
	Enabled bool
}

// AIExecutor runs a configured agent CLI in the workspace, then the same
// independent verification path as CommandExecutor (*Executor).
type AIExecutor struct {
	inner *Executor
	opts  AIExecutorOptions
	log   *slog.Logger
}

// NewAIExecutor wraps command executor behaviour with a templated AI CLI step.
func NewAIExecutor(inner *Executor, logger *slog.Logger, opts AIExecutorOptions) *AIExecutor {
	return &AIExecutor{inner: inner, opts: opts, log: logger}
}

// Cancel delegates to the underlying command executor.
func (a *AIExecutor) Cancel(attemptID uuid.UUID) bool {
	return a.inner.Cancel(attemptID)
}

// Run checks out the repo, runs the AI CLI template as the execute step, then
// verifies and finishes like a normal command job.
func (a *AIExecutor) Run(ctx context.Context, workerID uuid.UUID, claim workerapi.ClaimResponse) {
	if !a.opts.Enabled || strings.TrimSpace(a.opts.CommandTemplate) == "" {
		a.failEarly(ctx, workerID, claim, "AI executor is not enabled on this worker")
		return
	}
	cmd, err := expandAICommand(a.opts.CommandTemplate, claim.Spec)
	if err != nil {
		a.failEarly(ctx, workerID, claim, "expand AI command: "+err.Error())
		return
	}
	claim.Spec.Command = cmd
	a.inner.Run(ctx, workerID, claim)
}

func (a *AIExecutor) failEarly(ctx context.Context, workerID uuid.UUID, claim workerapi.ClaimResponse, msg string) {
	a.log.WarnContext(ctx, "AI executor rejected attempt", "attempt_id", claim.AttemptID, "err", msg)
	_ = a.inner.api.Finish(ctx, workerID, claim.AttemptID, workerapi.FinishRequest{
		Error: msg, Stage: workerapi.StageExecute,
	})
}

type aiTemplateData struct {
	Repository string
	Revision   string
	Command    string
	Prompt     string
	Acceptance string
}

func expandAICommand(tmpl string, spec workload.Spec) (string, error) {
	prompt := spec.Executor.Prompt
	if prompt == "" {
		prompt = strings.Join(spec.AcceptanceCriteria, "; ")
	}
	t, err := template.New("ai").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, aiTemplateData{
		Repository: spec.Repository,
		Revision:   spec.Revision,
		Command:    spec.Command,
		Prompt:     prompt,
		Acceptance: strings.Join(spec.AcceptanceCriteria, "; "),
	})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(buf.String())
	if out == "" {
		return "", fmt.Errorf("AI command template produced an empty command")
	}
	return out, nil
}

// SelectingRunner picks command vs AI executor from the claim's workload.
type SelectingRunner struct {
	Command JobRunner
	AI      JobRunner
}

// Run dispatches by spec.executor.kind (default command).
func (s SelectingRunner) Run(ctx context.Context, workerID uuid.UUID, claim workerapi.ClaimResponse) {
	if claim.Spec.Executor.Kind == workload.ExecutorAI && s.AI != nil {
		s.AI.Run(ctx, workerID, claim)
		return
	}
	s.Command.Run(ctx, workerID, claim)
}

// Cancel stops an attempt on both runners when they implement Canceler.
func (s SelectingRunner) Cancel(attemptID uuid.UUID) bool {
	var stopped bool
	if c, ok := s.Command.(Canceler); ok {
		stopped = c.Cancel(attemptID) || stopped
	}
	if c, ok := s.AI.(Canceler); ok {
		stopped = c.Cancel(attemptID) || stopped
	}
	return stopped
}

// WriteAgentContext is a small helper for tests and future resume support.
func WriteAgentContext(prompt, command string, exitCode int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"prompt": prompt, "command": command, "exit_code": exitCode,
	})
}
