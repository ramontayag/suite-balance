package balance

import (
	"fmt"
	"math"
	"reflect"
	"testing"
)

func testHistory(t *testing.T, files map[string]float64) *History {
	t.Helper()
	history := &History{}
	for file, duration := range files {
		if err := history.Record(file, Sample{RunID: 1, Attempt: 1, Seconds: duration}); err != nil {
			t.Fatal(err)
		}
	}
	return history
}

func TestHistoryRetainsLatestTenObservationsInRunOrder(t *testing.T) {
	history := &History{}
	for _, run := range []uint64{12, 3, 7, 1, 11, 4, 8, 2, 10, 9, 5, 6} {
		if err := history.Record("test/a", Sample{RunID: run, Attempt: 1, Seconds: float64(run)}); err != nil {
			t.Fatal(err)
		}
	}
	estimate, ok := history.Estimate("test/a")
	if !ok || estimate.Samples != 10 || math.Abs(estimate.Seconds-7.5) > 1e-12 {
		t.Fatalf("expected mean of runs 3 through 12: %+v", estimate)
	}
	if got := history.Samples("test/a"); got[0].RunID != 3 || got[9].RunID != 12 {
		t.Fatalf("unexpected window: %+v", got)
	}
}

func TestHistoryDeduplicatesRetriesAndRejectsConflictingDuplicates(t *testing.T) {
	history := &History{}
	for _, sample := range []Sample{{1, 1, 4}, {1, 2, 8}, {1, 1, 4}, {1, 2, 8}} {
		if err := history.Record("test/a", sample); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := history.Estimate("test/a"); got.Samples != 1 || got.Seconds != 8 {
		t.Fatalf("retry counted as a separate observation: %+v", got)
	}
	if err := history.Record("test/a", Sample{1, 2, 9}); err == nil {
		t.Fatal("conflicting records for the same run attempt must fail")
	}
	if got, _ := history.Estimate("test/a"); got.Seconds != 8 {
		t.Fatal("rejected sample changed history")
	}
}

func TestHistoryRejectsInvalidSamples(t *testing.T) {
	for _, seconds := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			history := &History{}
			if err := history.Record("test/a", Sample{1, 1, seconds}); err == nil {
				t.Fatal("expected invalid duration to fail")
			}
			if len(history.Samples("test/a")) != 0 {
				t.Fatal("invalid sample changed history")
			}
		})
	}
	for _, sample := range []Sample{{0, 1, 1}, {1, 0, 1}} {
		if err := (&History{}).Record("test/a", sample); err == nil {
			t.Fatal("run and attempt must be positive")
		}
	}
	if err := (&History{}).Record("../outside", Sample{1, 1, 1}); err == nil {
		t.Fatal("history paths must be repository-relative")
	}
}

func TestHistoryHandlesZeroDurationsAndLargeFiniteValues(t *testing.T) {
	history := &History{}
	for run := uint64(1); run <= 10; run++ {
		if err := history.Record("large", Sample{run, 1, math.MaxFloat64}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := history.Estimate("large"); math.IsInf(got.Seconds, 0) || math.Abs(got.Seconds/math.MaxFloat64-1) > 1e-14 {
		t.Fatalf("finite observations should produce a finite mean: %+v", got)
	}
	if err := history.Record("zero", Sample{1, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if got, ok := history.Estimate("zero"); !ok || got.Seconds != 0 || got.Samples != 1 {
		t.Fatalf("zero is a valid observation: %+v", got)
	}
}

func TestHistorySamplesCannotBeModifiedByCaller(t *testing.T) {
	history := testHistory(t, map[string]float64{"a": 4})
	samples := history.Samples("a")
	samples[0].Seconds = 100
	if got, _ := history.Estimate("a"); got.Seconds != 4 {
		t.Fatal("caller changed stored history")
	}
	if _, ok := history.Estimate("unknown"); ok {
		t.Fatal("unknown files must not have an estimate")
	}
}

func TestHistoryIsIndependentOfPublicationOrder(t *testing.T) {
	a, b := &History{}, &History{}
	for i := uint64(1); i <= 20; i++ {
		if err := a.Record("a", Sample{i, 1, float64(i)}); err != nil {
			t.Fatal(err)
		}
		if err := b.Record("a", Sample{21 - i, 1, float64(21 - i)}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(a.Samples("a"), b.Samples("a")) {
		t.Fatal("publication order changed the retained observations")
	}
}
