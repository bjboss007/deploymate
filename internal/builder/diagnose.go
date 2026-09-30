package builder

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrOutOfMemory marks a build the kernel/BuildKit killed for memory. The
// wrapped error still carries the builder's own evidence line.
var ErrOutOfMemory = errors.New("build ran out of memory")

// tailLines is how much builder output is kept for failure diagnosis: the
// cause is always near the end (BuildKit repeats the failing step's output
// and then prints its ERROR/solve lines).
const tailLines = 200

// outputTail wraps a line sink and remembers the last tailLines lines. Both
// stdout and stderr scanners write through it concurrently; it serializes
// them, so the sink never sees two lines at once.
type outputTail struct {
	mu    sync.Mutex
	lines []string
	next  func(string)
}

func newOutputTail(next func(string)) *outputTail { return &outputTail{next: next} }

func (t *outputTail) Line(s string) {
	t.mu.Lock()
	t.lines = append(t.lines, s)
	if len(t.lines) > tailLines {
		t.lines = t.lines[len(t.lines)-tailLines:]
	}
	t.next(s)
	t.mu.Unlock()
}

func (t *outputTail) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines...)
}

// oomMarkers are the ways an out-of-memory kill shows up in builder output
// (lowercased): the kernel/BuildKit ENOMEM, the container exit code for
// SIGKILL, and the runtimes' own heap errors. A Gradle daemon that
// "disappeared unexpectedly" inside a build container is almost always the
// OOM killer too.
var oomMarkers = []string{
	"cannot allocate memory",
	"resourceexhausted",
	"oomkilled",
	"out of memory",
	"outofmemoryerror",
	"exit code: 137",
	"javascript heap out of memory",
	"daemon disappeared unexpectedly",
}

// errorMarkers identify the builder's own summary of what failed, in the
// order BuildKit/buildx/railpack print them (the last match wins).
var errorMarkers = []string{"error:", "erro ", "failed to solve", "error "}

// maxCauseLen caps the cause quoted into the deployment error.
const maxCauseLen = 300

// buildError turns a failed builder process into an error that says why:
// an out-of-memory kill gets ErrOutOfMemory plus what to do about it;
// anything else quotes the builder's last error line. prefix is the
// historical message ("railpack build failed" / "build failed").
func buildError(prefix string, lines []string, runErr error) error {
	for i := len(lines) - 1; i >= 0; i-- {
		low := strings.ToLower(lines[i])
		for _, m := range oomMarkers {
			if strings.Contains(low, m) {
				return fmt.Errorf("%w — the build was killed for lack of memory (%q). "+
					"Give Docker more memory, lower the build's memory use (Gradle: org.gradle.jvmargs=-Xmx1g; "+
					"Node: NODE_OPTIONS=--max-old-space-size=1024), or stop other containers while building: %v",
					ErrOutOfMemory, clip(lines[i]), runErr)
			}
		}
	}
	if cause := lastErrorLine(lines); cause != "" {
		return fmt.Errorf("%s: %s: %w", prefix, cause, runErr)
	}
	return fmt.Errorf("%s: %w", prefix, runErr)
}

// lastErrorLine returns the last line that looks like the builder's error
// summary, trimmed and clipped; "" when none does.
func lastErrorLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		low := strings.ToLower(lines[i])
		for _, m := range errorMarkers {
			if strings.Contains(low, m) {
				return clip(lines[i])
			}
		}
	}
	return ""
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxCauseLen {
		s = s[:maxCauseLen] + "…"
	}
	return s
}
