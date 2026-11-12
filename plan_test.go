package balance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const testCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func makePlan(t *testing.T, files []string, coverage string) Plan {
	t.Helper()
	plan, err := NewPlan(PlanOptions{Files: files, Shards: 2, Coverage: coverage, Environment: "linux-standard", Commit: testCommit})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPlanRoundTripBindsCoverageToCommit(t *testing.T) {
	plan := makePlan(t, []string{"test/b", "test/a"}, "full")
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := ReadPlan(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, restored) {
		t.Fatal("plan changed during round trip")
	}
	if err := restored.Verify(testCommit); err != nil {
		t.Fatal(err)
	}
	if err := restored.Verify(strings.Repeat("b", 40)); err == nil {
		t.Fatal("a plan for another commit must fail")
	}
	if files, err := restored.Manifest(0); err != nil || !reflect.DeepEqual(files, []string{"test/a"}) {
		t.Fatalf("unexpected shard manifest: %v, %v", files, err)
	}
	if _, err := restored.Manifest(2); err == nil {
		t.Fatal("invalid shard must fail")
	}
}

func TestPlanRejectsTamperingAndInvalidAssignments(t *testing.T) {
	for _, mutation := range []func(*Plan){
		func(p *Plan) { p.Commit = strings.Repeat("b", 40) },
		func(p *Plan) { p.Allocation.Shards[0].Files = []string{} },
		func(p *Plan) { p.Allocation.Shards[1].Files = []string{"test/a"} },
		func(p *Plan) { p.Environment = "different-runner" },
	} {
		plan := makePlan(t, []string{"test/a", "test/b"}, "full")
		mutation(&plan)
		data, _ := json.Marshal(plan)
		if _, err := ReadPlan(bytes.NewReader(data)); err == nil {
			t.Fatal("tampered plan must fail")
		}
	}
	plan := makePlan(t, []string{"test/a", "test/b"}, "full")
	plan.Allocation.Shards[1].Files = []string{"test/a"}
	plan.Checksum, _ = plan.checksum()
	if err := plan.Verify(testCommit); err == nil {
		t.Fatal("even a rechecksummed plan must not duplicate or omit required files")
	}
}

func TestPlanValidatesRequiredMetadataAndResourceLimits(t *testing.T) {
	for _, options := range []PlanOptions{
		{Files: []string{"a"}, Shards: 2, Coverage: "full", Environment: "linux", Commit: "not-a-commit"},
		{Files: []string{"a"}, Shards: 2, Coverage: "full", Commit: testCommit},
		{Files: []string{"a"}, Shards: MaxShards + 1, Coverage: "full", Environment: "linux", Commit: testCommit},
	} {
		if _, err := NewPlan(options); err == nil {
			t.Fatalf("expected invalid options to fail: %+v", options)
		}
	}
}

func TestPlanCanForceVisibleFallback(t *testing.T) {
	plan, err := NewPlan(PlanOptions{Files: []string{"a", "b", "c"}, Shards: 2, Coverage: "full", Environment: "linux", Commit: testCommit, History: testHistory(t, map[string]float64{"a": 10, "b": 6, "c": 5}), FallbackReason: "History download failed."})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Allocation.FallbackReason != "History download failed." || !reflect.DeepEqual(plan.Allocation.Shards[0].Files, []string{"a", "c"}) {
		t.Fatalf("expected visible complete fallback: %+v", plan)
	}
}

func TestEmptySelectedPlanRoundTripsWithoutWorkers(t *testing.T) {
	plan := makePlan(t, nil, "selected")
	data, _ := json.Marshal(plan)
	if _, err := ReadPlan(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if plan.Allocation.HasTests || len(plan.Allocation.Shards) != 0 {
		t.Fatal("empty selection must not create workers")
	}
}

func TestPlanRejectsAssignmentsTooLargeForWorkersToRead(t *testing.T) {
	files := make([]string, 40000)
	for i := range files {
		files[i] = fmt.Sprintf("test/%s/%05d_test.rb", strings.Repeat("x", 100), i)
	}
	_, err := NewPlan(PlanOptions{Files: files, Shards: 2, Coverage: "full", Environment: "linux", Commit: testCommit})
	if err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("oversized assignment must fail before publication: %v", err)
	}
}
