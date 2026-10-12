package worker

import (
	"regexp"
	"strings"
)

// Integrity finding rule names reported to the server.
const (
	RuleDeletedTest    = "deleted_test"
	RuleRemovedAssert  = "removed_assert"
	RuleAddedSkip      = "added_skip"
	RuleAcceptanceEdit = "acceptance_edit"
)

var (
	testFileName   = regexp.MustCompile(`(?i)(^|/)([^/\s]*_test\.(go|py|js|ts|tsx)|test_[^/\s]+\.py|[^/\s]+\.(test|spec)\.(js|ts|tsx))(\s|$)`)
	assertLine     = regexp.MustCompile(`(?i)\b(assert\.|assert |t\.Error|t\.Fatal|t\.Fail|require\.|expect\()`)
	skipLine       = regexp.MustCompile(`(?i)\b(t\.Skip|xit\(|xdescribe\(|\.skip\(|pytest\.mark\.skip|@unittest\.skip)`)
	acceptancePath = regexp.MustCompile(`(?i)acceptance`)
)

// ScanDiff inspects a unified git diff (and optional name-status lines) for
// suspicious test or acceptance-criteria tampering. It is heuristic only.
func ScanDiff(nameStatus, diff string) []finding {
	var out []finding
	for _, line := range strings.Split(nameStatus, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		status, path := parts[0], parts[len(parts)-1]
		if (status == "D" || strings.HasPrefix(status, "R")) && testFileName.MatchString(path) {
			out = append(out, finding{Rule: RuleDeletedTest, Detail: path})
		}
		if acceptancePath.MatchString(path) {
			out = append(out, finding{Rule: RuleAcceptanceEdit, Detail: path})
		}
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") && assertLine.MatchString(line):
			out = append(out, finding{Rule: RuleRemovedAssert, Detail: strings.TrimSpace(line[1:])})
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") && skipLine.MatchString(line):
			out = append(out, finding{Rule: RuleAddedSkip, Detail: strings.TrimSpace(line[1:])})
		}
	}
	return dedupeFindings(out)
}

type finding struct {
	Rule   string
	Detail string
}

func dedupeFindings(in []finding) []finding {
	seen := map[string]struct{}{}
	out := make([]finding, 0, len(in))
	for _, f := range in {
		key := f.Rule + "\x00" + f.Detail
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, f)
	}
	return out
}
