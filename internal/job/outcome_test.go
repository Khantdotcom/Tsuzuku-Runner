package job

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

func code(n int32) *int32 { return &n }

func verified(exit int, timedOut bool, output string) workerapi.VerificationResult {
	start := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	return workerapi.VerificationResult{
		Name:       "verification",
		Command:    "make lint",
		Result:     workerapi.StepResult{ExitCode: exit, TimedOut: timedOut, DurationMS: 2500},
		Runtime:    workerapi.Runtime{Role: workerapi.RoleVerify, StartedAt: start, FinishedAt: start.Add(2500 * time.Millisecond)},
		OutputTail: output,
	}
}

func withVerify(cmd string) workload.Spec {
	return workload.Spec{Verification: &workload.Verification{Command: cmd}}
}

func TestJudge(t *testing.T) {
	cmd := "make lint"
	tests := []struct {
		name       string
		exit       *int32
		spec       workload.Spec
		results    []workerapi.VerificationResult
		integrity  *workerapi.IntegrityReport
		wantTo     State
		wantReason string
		wantCat    string
		wantRun    string
		wantChecks []string // statuses, in order
	}{
		{"success", code(0), workload.Spec{}, nil, nil, Completed, "command succeeded", "", CheckPassed, []string{CheckPassed}},
		{"command failed", code(3), workload.Spec{}, nil, nil, Failed, "command exited with code 3", CategoryTest, CheckFailed, []string{CheckFailed}},
		{"no exit code", nil, workload.Spec{}, nil, nil, Failed, "no exit code was recorded", CategoryInfrastructure, CheckError, []string{CheckError}},
		{
			"verified", code(0), withVerify(cmd),
			[]workerapi.VerificationResult{verified(0, false, "ok")},
			nil,
			Completed, "command and verification succeeded", "", CheckPassed,
			[]string{CheckPassed, CheckPassed},
		},
		{
			"verification failed", code(0), withVerify(cmd),
			[]workerapi.VerificationResult{verified(2, false, "lint failed")},
			nil,
			Failed, "verification exited with code 2", CategoryTest, CheckFailed,
			[]string{CheckPassed, CheckFailed},
		},
		{
			"verification timed out", code(0), withVerify(cmd),
			[]workerapi.VerificationResult{verified(137, true, "")},
			nil,
			Failed, "verification timed out after 2.5s", CategoryTimeout, CheckFailed,
			[]string{CheckPassed, CheckFailed},
		},
		{
			"verification skipped after failure", code(1), withVerify(cmd), nil, nil,
			Failed, "command exited with code 1", CategoryTest, CheckFailed,
			[]string{CheckFailed, CheckSkipped},
		},
		{
			"verification missing", code(0), withVerify(cmd), nil, nil,
			Failed, "verification did not run: verification", CategoryInfrastructure, CheckError,
			[]string{CheckPassed, CheckError},
		},
		{
			"unexpected verification ignored", code(0),
			workload.Spec{},
			[]workerapi.VerificationResult{verified(1, false, "")},
			nil,
			Completed, "command succeeded", "", CheckPassed,
			[]string{CheckPassed},
		},
		{
			"acceptance met", code(0),
			workload.Spec{
				AcceptanceCriteria: []string{"tests pass"},
				Verification:       &workload.Verification{Command: cmd},
			},
			[]workerapi.VerificationResult{verified(0, false, "all tests pass")},
			nil,
			Completed, "command, verification, and acceptance succeeded", "", CheckPassed,
			[]string{CheckPassed, CheckPassed, CheckPassed},
		},
		{
			"acceptance skipped without verify", code(0),
			workload.Spec{AcceptanceCriteria: []string{"tests pass"}},
			nil, nil,
			Completed, "command succeeded", "", CheckPassed,
			[]string{CheckPassed, CheckSkipped},
		},
		{
			"acceptance missed", code(0),
			workload.Spec{
				AcceptanceCriteria: []string{"tests pass"},
				Verification:       &workload.Verification{Command: cmd},
			},
			[]workerapi.VerificationResult{verified(0, false, "nope")},
			nil,
			Failed, "acceptance criterion not met: tests pass", CategoryRequirement, CheckFailed,
			[]string{CheckPassed, CheckPassed, CheckFailed},
		},
		{
			"acceptance skipped after verify fail", code(0),
			workload.Spec{
				AcceptanceCriteria: []string{"tests pass"},
				Verification:       &workload.Verification{Command: cmd},
			},
			[]workerapi.VerificationResult{verified(1, false, "lint failed")},
			nil,
			Failed, "verification exited with code 1", CategoryTest, CheckFailed,
			[]string{CheckPassed, CheckFailed, CheckSkipped},
		},
		{
			"integrity hit", code(0),
			workload.Spec{},
			nil,
			&workerapi.IntegrityReport{Findings: []workerapi.IntegrityFinding{{Rule: "deleted_test", Detail: "foo_test.go"}}},
			Failed, "integrity check failed: deleted_test", CategorySecurity, CheckFailed,
			[]string{CheckPassed, CheckFailed},
		},
		{
			"integrity clean", code(0),
			workload.Spec{},
			nil,
			&workerapi.IntegrityReport{},
			Completed, "command succeeded", "", CheckPassed,
			[]string{CheckPassed, CheckPassed},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := judge(tt.exit, tt.spec, tt.results, tt.integrity)
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
	v := judge(code(0), withVerify(cmd), []workerapi.VerificationResult{verified(2, false, "line\x00one\xff")}, nil)
	c := v.checks[1]
	if c.name != "verification" || c.kind != KindCommand || c.command == nil || *c.command != cmd {
		t.Errorf("check = %+v", c)
	}
	if c.exitCode == nil || *c.exitCode != 2 || c.startedAt == nil || c.finishedAt == nil {
		t.Errorf("check timing = %+v", c)
	}
	if c.output != "lineone\uFFFD" {
		t.Errorf("output = %q, want NUL removed and invalid UTF-8 replaced", c.output)
	}
	if v.details["exit_code"] != 2 {
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
		workerapi.StageCheckout:  CategoryNetwork,
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
