package balance

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReportReadsTheDocumentedFormat(t *testing.T) {
	report, err := ReadReport(strings.NewReader(`{
		"version": 1,
		"files": {
			"test/accounts_test.rb": {"duration_seconds": 12.4, "status": "passed"},
			"test/checkout_test.rb": {"duration_seconds": 38.1, "status": "failed"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Files["test/accounts_test.rb"]; got.Seconds != 12.4 || got.Status != "passed" {
		t.Fatalf("unexpected passing result: %+v", got)
	}
	if err := report.CheckFiles([]string{"test/accounts_test.rb", "test/checkout_test.rb"}); err != nil {
		t.Fatal(err)
	}
	if got := report.PassingFiles(); !reflect.DeepEqual(got, []string{"test/accounts_test.rb"}) {
		t.Fatalf("failed file must not contribute history: %v", got)
	}
}

func TestReportRejectsMalformedOrAmbiguousResults(t *testing.T) {
	cases := map[string]string{
		"missing version":      `{"files":{}}`,
		"unknown version":      `{"version":2,"files":{}}`,
		"missing files":        `{"version":1}`,
		"null files":           `{"version":1,"files":null}`,
		"unknown field":        `{"version":1,"files":{},"extra":true}`,
		"missing duration":     `{"version":1,"files":{"a":{"status":"passed"}}}`,
		"null duration":        `{"version":1,"files":{"a":{"duration_seconds":null,"status":"passed"}}}`,
		"negative duration":    `{"version":1,"files":{"a":{"duration_seconds":-1,"status":"passed"}}}`,
		"infinite duration":    `{"version":1,"files":{"a":{"duration_seconds":1e999,"status":"passed"}}}`,
		"unknown status":       `{"version":1,"files":{"a":{"duration_seconds":1,"status":"skipped"}}}`,
		"invalid path":         `{"version":1,"files":{"../a":{"duration_seconds":1,"status":"passed"}}}`,
		"unknown result field": `{"version":1,"files":{"a":{"duration_seconds":1,"status":"passed","extra":true}}}`,
		"duplicate result":     `{"version":1,"files":{"a":{"duration_seconds":1,"status":"passed"},"a":{"duration_seconds":2,"status":"passed"}}}`,
		"duplicate version":    `{"version":1,"version":1,"files":{}}`,
		"duplicate duration":   `{"version":1,"files":{"a":{"duration_seconds":1,"duration_seconds":2,"status":"passed"}}}`,
		"status case alias":    `{"version":1,"files":{"a":{"duration_seconds":1,"status":"failed","Status":"passed"}}}`,
		"files case alias":     `{"version":1,"files":{},"Files":{"a":{"duration_seconds":1,"status":"passed"}}}`,
		"trailing document":    `{"version":1,"files":{}} {"version":1,"files":{}}`,
		"incomplete JSON":      `{"version":1,"files":`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadReport(strings.NewReader(input)); err == nil {
				t.Fatal("expected invalid report to fail")
			}
		})
	}
}

func TestReportChecksExactlyTheAssignedFiles(t *testing.T) {
	report, err := ReadReport(strings.NewReader(`{"version":1,"files":{"a":{"duration_seconds":0,"status":"passed"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, files := range [][]string{{"a", "b"}, {"b"}, {}, {"a", "a"}} {
		if err := report.CheckFiles(files); err == nil {
			t.Fatalf("expected coverage mismatch for %v", files)
		}
	}
	if err := report.CheckFiles([]string{"a"}); err != nil {
		t.Fatal(err)
	}
}

func TestReportAllowsExplicitlyEmptyResults(t *testing.T) {
	report, err := ReadReport(strings.NewReader(`{"version":1,"files":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := report.CheckFiles(nil); err != nil {
		t.Fatal(err)
	}
}

func TestReportPassingFilesAreSorted(t *testing.T) {
	report, err := ReadReport(strings.NewReader(`{"version":1,"files":{"b":{"duration_seconds":2,"status":"passed"},"a":{"duration_seconds":1,"status":"passed"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := report.PassingFiles(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("passing files not sorted: %v", got)
	}
}

func TestReportRejectsOversizedInput(t *testing.T) {
	if _, err := ReadReport(strings.NewReader(strings.Repeat(" ", (8<<20)+1))); err == nil {
		t.Fatal("oversized report must fail before parsing")
	}
}

func TestReportReturnsReadErrors(t *testing.T) {
	want := errors.New("report download interrupted")
	if _, err := ReadReport(failingReader{want}); !errors.Is(err, want) {
		t.Fatalf("got %v, want wrapped read error", err)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func FuzzReadReport(f *testing.F) {
	f.Add(`{"version":1,"files":{"a":{"duration_seconds":0,"status":"passed"}}}`)
	f.Add(`{"version":1,"files":{}}`)
	f.Add(`null`)
	f.Fuzz(func(t *testing.T, input string) {
		report, err := ReadReport(strings.NewReader(input))
		if err != nil {
			return
		}
		for file, result := range report.Files {
			if err := validatePath(file); err != nil {
				t.Fatal(err)
			}
			if result.Status != "passed" && result.Status != "failed" {
				t.Fatalf("invalid result accepted: %+v", result)
			}
		}
	})
}
