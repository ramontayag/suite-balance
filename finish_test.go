package balance

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func makeReceipt(t *testing.T, plan Plan, index int, files map[string]FileResult) Receipt {
	t.Helper()
	r, err := NewReceipt(plan, index, 123, 1, Report{Version: 1, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFinishValidatesCoverageAndPublishesOnlyActualPassingFiles(t *testing.T) {
	plan := makePlan(t, []string{"test/a", "test/b"}, "full")
	a := makeReceipt(t, plan, 0, map[string]FileResult{"test/a": {10, "passed"}})
	b := makeReceipt(t, plan, 1, map[string]FileResult{"test/b": {20, "passed"}})
	data, _ := json.Marshal(a)
	restored, err := ReadReceipt(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	result := Finish(plan, source(), []Receipt{b, restored}, nil)
	if len(result.Issues) != 0 || result.Observation == nil || len(result.Observation.Files) != 2 {
		t.Fatalf("valid complete run rejected: %+v", result)
	}
	if !strings.Contains(result.Summary, "2 / 2") || !strings.Contains(result.Summary, "10.00s") {
		t.Fatalf("summary omits coverage or actual spread: %s", result.Summary)
	}
	if result.Observation.Files["test/a"].Seconds != 10 {
		t.Fatal("history must use actual execution, not estimates")
	}
}

func TestFinishLearnsPassingFilesDespiteAnotherFailureOrMissingShard(t *testing.T) {
	plan := makePlan(t, []string{"test/a", "test/b"}, "full")
	a := makeReceipt(t, plan, 0, map[string]FileResult{"test/a": {10, "passed"}})
	b := makeReceipt(t, plan, 1, map[string]FileResult{"test/b": {20, "failed"}})
	for _, receipts := range [][]Receipt{{a, b}, {a}} {
		result := Finish(plan, source(), receipts, nil)
		if len(result.Issues) == 0 || result.Observation == nil || len(result.Observation.Files) != 1 || result.Observation.Files["test/a"].Seconds != 10 {
			t.Fatalf("must fail coverage/tests while preserving the passing observation: %+v", result)
		}
	}
}

func TestFinishRejectsDuplicateShardAndMismatchedProvenance(t *testing.T) {
	plan := makePlan(t, []string{"test/a", "test/b"}, "full")
	for name, mutate := range map[string]func(*Receipt){
		"checksum":   func(r *Receipt) { r.PlanChecksum = "wrong" },
		"commit":     func(r *Receipt) { r.Commit = strings.Repeat("b", 40) },
		"run":        func(r *Receipt) { r.RunID++ },
		"attempt":    func(r *Receipt) { r.Attempt++ },
		"unassigned": func(r *Receipt) { r.Report.Files["test/b"] = FileResult{1, "passed"} },
	} {
		t.Run(name, func(t *testing.T) {
			a := makeReceipt(t, plan, 0, map[string]FileResult{"test/a": {10, "passed"}})
			mutate(&a)
			result := Finish(plan, source(), []Receipt{a}, nil)
			if len(result.Issues) == 0 || result.Observation != nil {
				t.Fatalf("invalid receipt contributed history: %+v", result)
			}
		})
	}
	a := makeReceipt(t, plan, 0, map[string]FileResult{"test/a": {10, "passed"}})
	result := Finish(plan, source(), []Receipt{a, a}, nil)
	if len(result.Issues) == 0 || result.Observation != nil {
		t.Fatal("duplicate shards must not contribute observations")
	}
}

func TestSelectedAndPullRequestRunsNeverPublishHistory(t *testing.T) {
	for _, variant := range []string{"selected", "pull_request", "feature"} {
		coverage := "full"
		if variant == "selected" {
			coverage = "selected"
		}
		plan := makePlan(t, []string{"test/a"}, coverage)
		s := source()
		s.Coverage = coverage
		if variant == "pull_request" {
			s.Event = variant
		}
		if variant == "feature" {
			s.Branch = variant
		}
		r := makeReceipt(t, plan, 0, map[string]FileResult{"test/a": {10, "passed"}})
		result := Finish(plan, s, []Receipt{r}, nil)
		if len(result.Issues) != 0 || result.Observation != nil {
			t.Fatalf("ineligible runs should validate without publication: %+v", result)
		}
	}
}

func TestFinishEmptySelectedRunSucceedsWithoutReceipts(t *testing.T) {
	plan := makePlan(t, nil, "selected")
	s := source()
	s.Coverage = "selected"
	result := Finish(plan, s, nil, nil)
	if len(result.Issues) != 0 || result.Observation != nil || !strings.Contains(result.Summary, "No tests selected") {
		t.Fatalf("unexpected empty selection result: %+v", result)
	}
}

func TestFinishPreservesPassingFilesInAPartiallyReportedShard(t *testing.T) {
	plan := makePlan(t, []string{"test/a", "test/b", "test/c"}, "full")
	a := makeReceipt(t, plan, 0, map[string]FileResult{"test/a": {12, "passed"}})
	b := makeReceipt(t, plan, 1, map[string]FileResult{"test/b": {7, "passed"}})
	result := Finish(plan, source(), []Receipt{a, b}, nil)
	if len(result.Issues) == 0 || result.Observation == nil || len(result.Observation.Files) != 2 {
		t.Fatalf("partial coverage must fail without losing valid observations: %+v", result)
	}
	if result.Observation.Files["test/a"].Seconds != 12 || !strings.Contains(result.Summary, "2 / 3") || !strings.Contains(result.Summary, "incomplete") {
		t.Fatalf("partial coverage summary/history incorrect: %+v", result)
	}
}

func TestFinishRejectsInvalidSourceAndTamperedPlans(t *testing.T) {
	for _, mutate := range []func(*Plan, *Source){
		func(_ *Plan, s *Source) { s.Commit = strings.Repeat("b", 40) },
		func(_ *Plan, s *Source) { s.Environment = "different" },
		func(_ *Plan, s *Source) { s.Coverage = "selected" },
		func(_ *Plan, s *Source) { s.RunID = 0 },
		func(p *Plan, _ *Source) { p.Checksum = "wrong" },
	} {
		plan := makePlan(t, []string{"test/a"}, "full")
		r := makeReceipt(t, plan, 0, map[string]FileResult{"test/a": {10, "passed"}})
		s := source()
		mutate(&plan, &s)
		result := Finish(plan, s, []Receipt{r}, nil)
		if len(result.Issues) == 0 || result.Observation != nil || result.Summary == "" {
			t.Fatalf("invalid provenance must fail visibly without publication: %+v", result)
		}
	}
}

func TestSummaryDoesNotReportSpreadFromOnlySomeShardTotals(t *testing.T) {
	plan := makePlan(t, []string{"test/a", "test/b", "test/c"}, "full")
	a := makeReceipt(t, plan, 0, map[string]FileResult{"test/a": {math.MaxFloat64, "passed"}, "test/c": {math.MaxFloat64, "passed"}})
	b := makeReceipt(t, plan, 1, map[string]FileResult{"test/b": {1, "passed"}})
	result := Finish(plan, source(), []Receipt{a, b}, nil)
	if strings.Contains(result.Summary, "Actual spread: **") || !strings.Contains(result.Summary, "Actual spread unavailable") {
		t.Fatalf("summary must not compute spread from only the finite shard total: %s", result.Summary)
	}
}
