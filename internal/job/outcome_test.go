package job

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

func code(n int32) *int32 { return &n }

func verified(exit int, timedOut bool, output string) *workerapi.VerificationResult {
	start := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	return &workerapi.VerificationResult{
		Command:    "make lint",
		Result:     workerapi.StepResult{ExitCode: exit, TimedOut: timedOut, DurationMS: 2500},
		Runtime:    workerapi.Runtime{Role: workerapi.RoleVerify, StartedAt: start, FinishedAt: start.Add(2500 * time.Millisecond)},
		OutputTail: output,
	}
}

func TestJudge(t *testing.T) {
	cmd := "make lint"
	tests := []struct {
		name       string
		exit       *int32
		verifyCmd  *string
		v          *workerapi.VerificationResult
		wantTo     State
		wantReason string
		wantCat    string
		wantRun    string
		wantChecks []string // statuses, in order
	}{
		{"success", code(0), nil, nil, Completed, "command succeeded", "", CheckPassed, []string{CheckPassed}},
		{"command failed", code(3), nil, nil, Failed, "command exited with code 3", CategoryTest, CheckFailed, []string{CheckFailed}},
		{"no exit code", nil, nil, nil, Failed, "no exit code was recorded", CategoryInfrastructure, CheckError, []string{CheckError}},
		{
			"verified", code(0), &cmd, verified(0, false, "ok"),
			Completed, "command and verification succeeded", "", CheckPassed,
			[]string{CheckPassed, CheckPassed},
		},
		{
			"verification failed", code(0), &cmd, verified(2, false, "lint failed"),
			Failed, "verification exited with code 2", CategoryTest, CheckFailed,
			[]string{CheckPassed, CheckFailed},
		},
		{
			"verification timed out", code(0), &cmd, verified(137, true, ""),
			Failed, "verification timed out after 2.5s", CategoryTimeout, CheckFailed,
			[]string{CheckPassed, CheckFailed},
		},
		{
			"verification skipped after failure", code(1), &cmd, nil,
			Failed, "command exited with code 1", CategoryTest, CheckFailed,
			[]string{CheckFailed, CheckSkipped},
		},
		{
			"verification missing", code(0), &cmd, nil,
			Failed, "verification did not run", CategoryInfrastructure, CheckError,
			[]string{CheckPassed, CheckError},
		},
		{
			"unexpected verification ignored", code(0), nil, verified(1, false, ""),
			Completed, "command succeeded", "", CheckPassed,
			[]string{CheckPassed},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := judge(tt.exit, tt.verifyCmd, tt.v)
			if v.to != tt.wantTo || v.reason != tt.wantReason || v.category != tt.wantCat || v.runStatus != tt.wantRun {
				t.Errorf("verdict = %s %q %q run %s; want %s %q %q run %s",
					v.to, v.reason, v.category, v.runStatus, tt.wantTo, tt.wantReason, tt.wantCat, tt.wantRun)
			}
			wantStatus := AttemptFailed
			if tt.wantTo == Completed {
				wantStatus = AttemptSucceeded
			}
			if v.status != wantStatus {
				t.Errorf("attempt status = %s, want %s", v.status, wantStatus)
			}
			if len(v.checks) != len(tt.wantChecks) {
				t.Fatalf("checks = %+v", v.checks)
			}
			for i, want := range tt.wantChecks {
				if v.checks[i].status != want {
					t.Errorf("check %s = %s, want %s", v.checks[i].name, v.checks[i].status, want)
				}
			}
		})
	}
}

func TestJudgeRecordsVerificationDetails(t *testing.T) {
	cmd := "make lint"
	v := judge(code(0), &cmd, verified(2, false, "line\x00one\xff"))
	c := v.checks[1]
	if c.name != CheckNameVerification || c.kind != KindCommand || c.command == nil || *c.command != cmd {
		t.Errorf("check = %+v", c)
	}
	if c.exitCode == nil || *c.exitCode != 2 || c.startedAt == nil || c.finishedAt == nil {
		t.Errorf("check timing = %+v", c)
	}
	if c.output != "lineone\uFFFD" {
		t.Errorf("output = %q, want NUL removed and invalid UTF-8 replaced", c.output)
	}
	if v.details["verification_exit_code"] != 2 {
		t.Errorf("details = %v", v.details)
	}
}

func TestStageCategory(t *testing.T) {
	tests := map[string]string{
		workerapi.StageImage:     CategoryEnvironment,
		workerapi.StageWorkspace: CategoryInfrastructure,
		workerapi.StageExecute:   CategoryInfrastructure,
		workerapi.StageVerify:    CategoryInfrastructure,
		workerapi.StageShutdown:  CategoryInfrastructure,
		workerapi.StageCheckout:  CategoryUnknown,
		"":                       CategoryUnknown,
	}
	for stage, want := range tests {
		if got := stageCategory(stage); got != want {
			t.Errorf("stageCategory(%q) = %s, want %s", stage, got, want)
		}
	}
}

func TestCleanOutputKeepsTheEnd(t *testing.T) {
	long := strings.Repeat("é", maxCheckOutput) + "THE END"
	got := cleanOutput(long)
	if len(got) > maxCheckOutput+3 || !strings.HasSuffix(got, "THE END") || !strings.HasPrefix(got, "...") {
		t.Errorf("len %d, suffix ok %v", len(got), strings.HasSuffix(got, "THE END"))
	}
	if !utf8.ValidString(got) {
		t.Error("cut split a UTF-8 character")
	}
}
