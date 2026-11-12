package balance

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestAllocationJSONUsesEmptyCollectionsInsteadOfNull(t *testing.T) {
	for _, coverage := range []string{"full", "selected"} {
		var files []string
		if coverage == "full" {
			files = []string{"a"}
		}
		plan, err := Allocate(files, 2, coverage, nil)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		var output map[string]json.RawMessage
		if err := json.Unmarshal(data, &output); err != nil {
			t.Fatal(err)
		}
		if string(output["estimates"]) != "{}" {
			t.Fatalf("expected an empty estimates object: %s", data)
		}
		if coverage == "selected" && string(output["shards"]) != "[]" {
			t.Fatalf("expected an empty shards array: %s", data)
		}
	}
}

func TestAllocationBalancesUnevenWork(t *testing.T) {
	files := []string{"a_test", "b_test", "c_test"}
	history := testHistory(t, map[string]float64{"a_test": 10, "b_test": 6, "c_test": 5})
	plan, err := Allocate(files, 2, "full", history)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Shards; !reflect.DeepEqual(got[0].Files, []string{"a_test"}) || !reflect.DeepEqual(got[1].Files, []string{"b_test", "c_test"}) {
		t.Fatalf("unexpected allocation: %+v", got)
	}
	if plan.Shards[0].PredictedSeconds != 10 || plan.Shards[1].PredictedSeconds != 11 {
		t.Fatalf("unexpected predictions: %+v", plan.Shards)
	}
	if plan.FallbackReason != "" || !plan.HasTests {
		t.Fatalf("expected history-based allocation: %+v", plan)
	}
}

func TestAllocationIsIndependentOfInputOrder(t *testing.T) {
	history := testHistory(t, map[string]float64{"a": 3, "b": 3, "c": 3, "d": 3})
	a, err := Allocate([]string{"d", "b", "a", "c"}, 3, "full", history)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Allocate([]string{"c", "a", "b", "d"}, 3, "full", history)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("input order changed allocation: %+v vs %+v", a, b)
	}
	want := [][]string{{"a", "d"}, {"b"}, {"c"}}
	for i, shard := range a.Shards {
		if !reflect.DeepEqual(shard.Files, want[i]) {
			t.Fatalf("shard %d: got %v, want %v", i, shard.Files, want[i])
		}
	}
}

func TestAllocationUsesMedianForNewFiles(t *testing.T) {
	history := testHistory(t, map[string]float64{"a": 2, "b": 8, "c": 10, "d": 20})
	plan, err := Allocate([]string{"a", "b", "c", "d", "new"}, 2, "full", history)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Estimates["new"]; got.Seconds != 9 || got.Samples != 0 {
		t.Fatalf("new-file estimate: %+v", got)
	}
	if got := plan.Estimates["b"]; got.Seconds != 8 || got.Samples != 1 {
		t.Fatalf("known-file estimate: %+v", got)
	}
}

func TestAllocationFallsBackWithoutUsableHistory(t *testing.T) {
	for _, history := range []*History{nil, {}, testHistory(t, map[string]float64{"deleted": 100})} {
		plan, err := Allocate([]string{"c", "a", "b"}, 2, "full", history)
		if err != nil {
			t.Fatal(err)
		}
		if plan.FallbackReason == "" {
			t.Fatal("fallback must explain why history was unavailable")
		}
		if !reflect.DeepEqual(plan.Shards[0].Files, []string{"a", "c"}) || !reflect.DeepEqual(plan.Shards[1].Files, []string{"b"}) {
			t.Fatalf("expected sorted round-robin: %+v", plan.Shards)
		}
	}
}

func TestEmptySelectedRunNeedsNoWorkers(t *testing.T) {
	plan, err := Allocate(nil, 3, "selected", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasTests || len(plan.Shards) != 0 {
		t.Fatalf("empty selection should not start workers: %+v", plan)
	}
	if _, err := Allocate(nil, 3, "full", nil); err == nil {
		t.Fatal("an empty full-suite list must fail")
	}
}

func TestAllocationRejectsInvalidRequiredInputs(t *testing.T) {
	tests := []struct {
		name, coverage string
		files          []string
		shards         int
	}{
		{"zero shards", "full", []string{"a"}, 0},
		{"negative shards", "full", []string{"a"}, -1},
		{"too many shards", "full", []string{"a"}, MaxShards + 1},
		{"unknown coverage", "partial", []string{"a"}, 2},
		{"duplicate file", "full", []string{"a", "a"}, 2},
		{"absolute path", "full", []string{"/rails/a"}, 2},
		{"parent directory", "full", []string{"../a"}, 2},
		{"embedded parent", "full", []string{"test/../a"}, 2},
		{"windows path", "full", []string{`C:\test\a`}, 2},
		{"line separator", "full", []string{"test/a\ntest/b"}, 2},
		{"empty path", "full", []string{""}, 2},
		{"current directory", "full", []string{"."}, 2},
		{"parent itself", "full", []string{".."}, 2},
		{"invalid UTF-8 path", "full", []string{"test/\xff"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Allocate(tt.files, tt.shards, tt.coverage, nil); err == nil {
				t.Fatal("expected an error, not fallback")
			}
		})
	}
}

func TestAllocationPreservesPathsWithSpacesAndInputFiles(t *testing.T) {
	files := []string{"test/with spaces_test.rb", "test/a_test.rb"}
	original := append([]string(nil), files...)
	plan, err := Allocate(files, 3, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, original) {
		t.Fatal("allocation modified the caller's file list")
	}
	if plan.Shards[1].Files[0] != files[0] {
		t.Fatalf("filename was changed: %+v", plan.Shards)
	}
}

func TestAllocationRejectsOverflowInPredictedTotals(t *testing.T) {
	history := testHistory(t, map[string]float64{"a": math.MaxFloat64, "b": math.MaxFloat64})
	plan, err := Allocate([]string{"a", "b"}, 1, "full", history)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.FallbackReason, "overflow") {
		t.Fatalf("unusable predictions need a visible fallback: %+v", plan)
	}
}

func TestAllocationAlwaysPartitionsRequiredFiles(t *testing.T) {
	for count := 1; count <= 100; count++ {
		files := make([]string, count)
		for i := range files {
			files[i] = strings.Repeat("a", i+1)
		}
		for shards := 1; shards <= 7; shards++ {
			plan, err := Allocate(files, shards, "full", nil)
			if err != nil {
				t.Fatal(err)
			}
			seen := make(map[string]int)
			for _, shard := range plan.Shards {
				for _, file := range shard.Files {
					seen[file]++
				}
			}
			for _, file := range files {
				if seen[file] != 1 {
					t.Fatalf("%q assigned %d times", file, seen[file])
				}
			}
		}
	}
}
