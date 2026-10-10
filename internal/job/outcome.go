package job

import (
	"fmt"
	"strings"
	"time"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

// Failure categories, matching failures.category.
const (
	CategoryTest           = "TEST"
	CategoryEnvironment    = "ENVIRONMENT"
	CategoryInfrastructure = "INFRASTRUCTURE"
	CategoryTimeout        = "TIMEOUT"
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
	KindExitCode = "exit_code"
	KindCommand  = "command"

	CheckNameExitCode     = "exit_code"
	CheckNameVerification = "verification"
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

// judge decides a job's outcome from the command's exit code, the workload's
// verification command (nil if it has none), and the verification result the
// worker reported (nil if it ran none).
func judge(exitCode *int32, verifyCmd *string, v *workerapi.VerificationResult) verdict {
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

	if verifyCmd != nil {
		vc := check{name: CheckNameVerification, kind: KindCommand, command: verifyCmd}
		switch {
		case exitCheck.status != CheckPassed:
			vc.status, vc.output = CheckSkipped, "skipped because the command did not succeed"
		case v == nil:
			vc.status, vc.output = CheckError, "the worker reported no verification result"
			out = failed("verification did not run", CategoryInfrastructure)
		default:
			code := int32(v.Result.ExitCode) //nolint:gosec // exit codes are small
			started, finished := v.Runtime.StartedAt, v.Runtime.FinishedAt
			vc.exitCode, vc.startedAt, vc.finishedAt = &code, &started, &finished
			vc.output = cleanOutput(v.OutputTail)
			switch {
			case v.Result.TimedOut:
				vc.status = CheckFailed
				out = failed("verification timed out after "+durationText(v.Result.DurationMS), CategoryTimeout)
			case v.Result.ExitCode != 0:
				vc.status = CheckFailed
				out = failed(fmt.Sprintf("verification exited with code %d", v.Result.ExitCode), CategoryTest)
				out.details = map[string]any{"verification_exit_code": v.Result.ExitCode}
			default:
				vc.status = CheckPassed
				out.reason = "command and verification succeeded"
			}
		}
		checks = append(checks, vc)
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
