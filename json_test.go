package balance

import (
	"strings"
	"testing"
)

func TestStrictDecoderRejectsWrongContainersAndExcessiveNesting(t *testing.T) {
	for _, input := range []string{
		`{"version":1,"files":[]}`,
		`{"version":1,"files":{"a":[]}}`,
		`{"version":{},"files":{}}`,
		strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40),
	} {
		if _, err := ReadReport(strings.NewReader(input)); err == nil {
			t.Fatalf("invalid container must fail: %s", input)
		}
	}
}

func TestStrictDecoderRejectsInvalidUTF8InsteadOfReplacingFileNames(t *testing.T) {
	input := "{\"version\":1,\"files\":{\"test/\xff\":{\"duration_seconds\":1,\"status\":\"passed\"}}}"
	if _, err := ReadReport(strings.NewReader(input)); err == nil {
		t.Fatal("invalid UTF-8 must fail instead of changing the reported filename")
	}
}
