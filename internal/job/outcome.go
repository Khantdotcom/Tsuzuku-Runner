package job

import (
	"fmt"
	"strings"
	"time"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

// Failure categories, matching failures.category.
const (
	CategoryRequirement    = "REQUIREMENT"
	CategoryExecutor       = "EXECUTOR"
	CategoryCode           = "CODE"
	CategoryTest           = "TEST"
	CategoryDependency     = "DEPENDENCY"
	CategoryEnvironment    = "ENVIRONMENT"
	CategoryResource       = "RESOURCE"
	CategoryInfrastructure = "INFRASTRUCTURE"
	CategoryNetwork        = "NETWORK"
	CategoryTimeout        = "TIMEOUT"
	CategorySecurity       = "SECURITY"
	CategoryUnknown        = "UNKNOWN"
)

// Verification statuses, matching verification_runs.status and
// verification_checks.status.
const (
	CheckPassed  = "PASSED"
	CheckFailed  = "FAILED"
	CheckError   = "ERROR"
	CheckSkipped = "SKIPPED"
)

// Verification check kinds and names.
const (
	KindExitCode   = "exit_code"
	KindCommand    = "command"
	KindAcceptance = "acceptance"
	KindIntegrity  = "integrity"

	CheckNameExitCode = "exit_code"
)

// maxCheckOutput bounds the output kept on a verification check.
const maxCheckOutput = 16 << 10

// outcome is how an attempt ends.
type outcome struct {
	to     State
	status string // attempt status
	reason string
	// category is the failure category; empty when nothing failed.
	category string
	details  map[string]any
}

// check is one verification check to record.
type check struct {
	name, kind, status string
	command            *string
	exitCode           *int32
	output             string
	startedAt          *time.Time
	finishedAt         *time.Time
}

// verdict is the outcome of a verified attempt with the checks behind it.
type verdict struct {
	outcome
	runStatus string
	checks    []check
}

// judge decides a job's outcome from the command's exit code, the workload
// Spec, verification results the worker reported, and an optional integrity
// scan. It is a pure function with no database or network.
func judge(exitCode *int32, spec workload.Spec, results []workerapi.VerificationResult, integrity *workerapi.IntegrityReport) verdict {
	exitCheck := check{name: CheckNameExitCode, kind: KindExitCode, exitCode: exitCode}
	var out outcome
	switch {
	case exitCode == nil:
		exitCheck.status, exitCheck.output = CheckError, "no exit code was recorded"
		out = failed("no exit code was recorded", CategoryInfrastructure)
	case *exitCode != 0:
		exitCheck.status = CheckFailed
		exitCheck.output = fmt.Sprintf("command exited with code %d", *exitCode)
		out = failed(exitCheck.output, CategoryTest)
		out.details = map[string]any{"exit_code": *exitCode}
	default:
		exitCheck.status, exitCheck.output = CheckPassed, "command exited with code 0"
		out = outcome{to: Completed, status: AttemptSucceeded, reason: "command succeeded"}
	}
	checks := []check{exitCheck}
	commandOK := exitCheck.status == CheckPassed

	byName := map[string]workerapi.VerificationResult{}
	for _, r := range results {
		name := r.Name
		if name == "" {
			name = "verification"
		}
		byName[name] = r
	}

	steps := spec.VerificationSteps()
	var outputParts []string
	for _, step := range steps {
		cmd := step.Command
		vc := check{name: step.Name, kind: KindCommand, command: &cmd}
		r, ok := byName[step.Name]
		switch {
		case !commandOK:
			vc.status, vc.output = CheckSkipped, "skipped because the command did not succeed"
		case !ok:
			vc.status, vc.output = CheckError, "the worker reported no verification result"
			out = failed("verification did not run: "+step.Name, CategoryInfrastructure)
		default:
			code := int32(r.Result.ExitCode) //nolint:gosec // exit codes are small
			started, finished := r.Runtime.StartedAt, r.Runtime.FinishedAt
			vc.exitCode, vc.startedAt, vc.finishedAt = &code, &started, &finished
			vc.output = cleanOutput(r.OutputTail)
			outputParts = append(outputParts, r.OutputTail)
			switch {
			case r.Result.TimedOut:
				vc.status = CheckFailed
				out = failed(step.Name+" timed out after "+durationText(r.Result.DurationMS), CategoryTimeout)
			case r.Result.ExitCode != 0:
				vc.status = CheckFailed
				out = failed(fmt.Sprintf("%s exited with code %d", step.Name, r.Result.ExitCode), CategoryTest)
				out.details = map[string]any{"check": step.Name, "exit_code": r.Result.ExitCode}
			default:
				vc.status = CheckPassed
			}
		}
		checks = append(checks, vc)
	}

	combined := strings.Join(outputParts, "\n")
	verifyOK := commandOK && out.to == Completed
	for i, criterion := range spec.AcceptanceCriteria {
		name := fmt.Sprintf("acceptance[%d]", i)
		ac := check{name: name, kind: KindAcceptance, output: criterion}
		switch {
		case !commandOK:
			ac.status, ac.output = CheckSkipped, "skipped because the command did not succeed"
		case len(steps) == 0:
			ac.status, ac.output = CheckSkipped, "skipped because no verification command was configured"
		case !verifyOK:
			ac.status, ac.output = CheckSkipped, "skipped because verification did not pass"
		case strings.Contains(combined, criterion):
			ac.status = CheckPassed
			ac.output = "found in verification output: " + criterion
		default:
			ac.status = CheckFailed
			ac.output = "not found in verification output: " + criterion
			out = failed("acceptance criterion not met: "+criterion, CategoryRequirement)
			out.details = map[string]any{"criterion": criterion}
			verifyOK = false
		}
		checks = append(checks, ac)
	}

	if integrity != nil {
		for i, f := range integrity.Findings {
			name := fmt.Sprintf("integrity[%d]", i)
			ic := check{
				name: name, kind: KindIntegrity, status: CheckFailed,
				output: f.Rule + ": " + f.Detail,
			}
			checks = append(checks, ic)
			if out.to == Completed {
				out = failed("integrity check failed: "+f.Rule, CategorySecurity)
				out.details = map[string]any{"rule": f.Rule, "detail": f.Detail}
			}
		}
		if len(integrity.Findings) == 0 {
			checks = append(checks, check{
				name: "integrity", kind: KindIntegrity, status: CheckPassed,
				output: "no suspicious changes in workspace diff",
			})
		}
	}

	if out.to == Completed && len(steps) > 0 {
		out.reason = "command and verification succeeded"
		for _, c := range checks {
			if c.kind == KindAcceptance && c.status == CheckPassed {
				out.reason = "command, verification, and acceptance succeeded"
				break
			}
		}
	}

	return verdict{outcome: out, runStatus: runStatus(checks), checks: checks}
}

// runStatus summarises checks: any error wins over any failure.
func runStatus(checks []check) string {
	status := CheckPassed
	for _, c := range checks {
		switch c.status {
		case CheckError:
			return CheckError
		case CheckFailed:
			status = CheckFailed
		}
	}
	return status
}

func failed(reason, category string) outcome {
	return outcome{to: Failed, status: AttemptFailed, reason: reason, category: category}
}

// stageCategory classifies a failure the worker reported by the stage that
// failed. A checkout can fail because of a bad revision or a network
// problem, which the worker cannot tell apart.
func stageCategory(stage string) string {
	switch stage {
	case workerapi.StageImage:
		return CategoryEnvironment
	case workerapi.StageCheckout:
		return CategoryNetwork
	case workerapi.StageWorkspace, workerapi.StageExecute, workerapi.StageVerify, workerapi.StageShutdown:
		return CategoryInfrastructure
	default:
		return CategoryUnknown
	}
}

// cleanOutput makes command output storable as text: valid UTF-8, no NUL
// bytes, and at most maxCheckOutput bytes, keeping the end.
func cleanOutput(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
	if len(s) <= maxCheckOutput {
		return s
	}
	cut := len(s) - maxCheckOutput
	for cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut++
	}
	return "..." + s[cut:]
}
