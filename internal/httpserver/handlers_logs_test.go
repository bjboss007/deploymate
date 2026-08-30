package httpserver

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
)

// TestWriteContainerLogs verifies the shared demux helper splits a docker
// stdcopy-multiplexed stream into SSE lines, tagging stderr. This is the
// codepath both the live follow tail and the stopped-container snapshot use.
func TestWriteContainerLogs(t *testing.T) {
	var raw bytes.Buffer
	out := stdcopy.NewStdWriter(&raw, stdcopy.Stdout)
	errw := stdcopy.NewStdWriter(&raw, stdcopy.Stderr)
	if _, err := out.Write([]byte("listening on :8080\n")); err != nil {
		t.Fatalf("write stdout: %v", err)
	}
	if _, err := errw.Write([]byte("panic: boom\n")); err != nil {
		t.Fatalf("write stderr: %v", err)
	}

	rec := httptest.NewRecorder()
	writeContainerLogs(rec, &raw)

	body := rec.Body.String()
	if !strings.Contains(body, "data: listening on :8080") {
		t.Errorf("stdout line missing from SSE output:\n%s", body)
	}
	if !strings.Contains(body, "data: [stderr] panic: boom") {
		t.Errorf("stderr line missing or untagged in SSE output:\n%s", body)
	}
}
