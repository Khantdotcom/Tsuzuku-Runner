package worker

import "testing"

func TestScanDiff(t *testing.T) {
	nameStatus := "D\tfoo_test.go\nM\tdocs/acceptance.md\n"
	diff := `--- a/main_test.go
+++ b/main_test.go
@@ -1,5 +1,5 @@
-	t.Error("boom")
+	t.Skip("later")
`
	got := ScanDiff(nameStatus, diff)
	rules := map[string]bool{}
	for _, f := range got {
		rules[f.Rule] = true
	}
	for _, want := range []string{RuleDeletedTest, RuleAcceptanceEdit, RuleRemovedAssert, RuleAddedSkip} {
		if !rules[want] {
			t.Errorf("missing rule %s in %+v", want, got)
		}
	}
}

func TestScanDiffClean(t *testing.T) {
	if got := ScanDiff("M\tmain.go\n", "+fmt.Println(\"ok\")\n"); len(got) != 0 {
		t.Fatalf("clean diff findings = %+v", got)
	}
}
