package worker

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

func TestExpandAICommand(t *testing.T) {
	got, err := expandAICommand(`echo {{.Prompt}} @ {{.Revision}}`, workload.Spec{
		Revision: "main",
		Executor: workload.Executor{Prompt: "fix tests"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "echo fix tests @ main" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteAgentContext(t *testing.T) {
	b, err := WriteAgentContext("p", "cmd", 0)
	if err != nil || len(b) == 0 {
		t.Fatalf("%v %s", err, b)
	}
}

type stubRunner struct {
	last string
}

func (s *stubRunner) Run(_ context.Context, _ uuid.UUID, claim workerapi.ClaimResponse) {
	s.last = claim.Spec.Executor.Kind
}

func TestSelectingRunnerDispatchesAI(t *testing.T) {
	cmd := &stubRunner{}
	ai := &stubRunner{}
	sel := SelectingRunner{Command: cmd, AI: ai}
	sel.Run(context.Background(), uuid.Nil, workerapi.ClaimResponse{
		Spec: workload.Spec{Executor: workload.Executor{Kind: workload.ExecutorAI}},
	})
	if ai.last != workload.ExecutorAI || cmd.last != "" {
		t.Fatalf("ai=%q cmd=%q", ai.last, cmd.last)
	}
	sel.Run(context.Background(), uuid.Nil, workerapi.ClaimResponse{
		Spec: workload.Spec{Executor: workload.Executor{Kind: workload.ExecutorCommand}},
	})
	if cmd.last != workload.ExecutorCommand {
		t.Fatalf("command last=%q", cmd.last)
	}
}
