package job

import (
	"testing"
	"time"
)

func TestActionFor(t *testing.T) {
	tests := []struct {
		cat  string
		want FailureAction
	}{
		{CategoryInfrastructure, ActionRetry},
		{CategoryNetwork, ActionRetry},
		{CategoryTimeout, ActionRetry},
		{CategoryResource, ActionRetry},
		{CategoryEnvironment, ActionRetry},
		{CategoryRequirement, ActionBlock},
		{CategoryTest, ActionFail},
		{CategoryCode, ActionFail},
		{CategoryUnknown, ActionFail},
		{CategorySecurity, ActionFail},
		{"", ActionFail},
	}
	for _, tt := range tests {
		if got := ActionFor(tt.cat); got != tt.want {
			t.Errorf("ActionFor(%q) = %v, want %v", tt.cat, got, tt.want)
		}
	}
}

func TestRetryBackoff(t *testing.T) {
	base, backoffCap := time.Second, 5*time.Minute
	if got := RetryBackoff(1, base, backoffCap); got != time.Second {
		t.Errorf("attempt 1 = %v, want 1s", got)
	}
	if got := RetryBackoff(2, base, backoffCap); got != 2*time.Second {
		t.Errorf("attempt 2 = %v, want 2s", got)
	}
	if got := RetryBackoff(3, base, backoffCap); got != 4*time.Second {
		t.Errorf("attempt 3 = %v, want 4s", got)
	}
	if got := RetryBackoff(20, base, backoffCap); got != backoffCap {
		t.Errorf("attempt 20 = %v, want cap %v", got, backoffCap)
	}
}

func TestApplyPolicy(t *testing.T) {
	base, backoffCap := time.Second, 5*time.Minute
	o := failed("boom", CategoryInfrastructure)
	got, when := applyPolicy(o, 1, 3, base, backoffCap)
	if got.to != Retrying || when == nil {
		t.Fatalf("retryable under max: to=%s when=%v", got.to, when)
	}
	got, when = applyPolicy(o, 3, 3, base, backoffCap)
	if got.to != Failed || when != nil {
		t.Fatalf("retryable at max: to=%s when=%v", got.to, when)
	}
	got, when = applyPolicy(failed("need human", CategoryRequirement), 1, 3, base, backoffCap)
	if got.to != Blocked || when != nil {
		t.Fatalf("requirement: to=%s when=%v", got.to, when)
	}
	got, when = applyPolicy(failed("tests", CategoryTest), 1, 3, base, backoffCap)
	if got.to != Failed || when != nil {
		t.Fatalf("test: to=%s when=%v", got.to, when)
	}
	ok := outcome{to: Completed, status: AttemptSucceeded, reason: "ok"}
	got, when = applyPolicy(ok, 1, 3, base, backoffCap)
	if got.to != Completed || when != nil {
		t.Fatalf("success: to=%s when=%v", got.to, when)
	}
}
