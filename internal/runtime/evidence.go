package runtime

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
)

// maxEvidenceLine caps one quoted log line.
const maxEvidenceLine = 300

// Evidence says why a container is not serving, for a deploy that is about
// to throw it away: a one-sentence summary of its state (killed for memory /
// exited with code N / still running but silent) and its last log lines.
// Best-effort and bounded — a failure to gather it returns what it has;
// it never fails a deploy that is already failing.
func Evidence(ctx context.Context, rt Runtime, name string, tail int) (summary string, lines []string) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	if info, err := rt.Inspect(ctx, name); err == nil {
		switch {
		case info.OOMKilled:
			summary = "the container was killed for lack of memory"
		case !info.Running && info.State != "" && info.State != "created":
			summary = fmt.Sprintf("the container exited with code %d", info.ExitCode)
		case info.Running:
			summary = "the container is running but nothing answered on its port"
		}
	}
	rd, err := rt.Logs(ctx, name, false, tail)
	if err != nil || rd == nil {
		return summary, nil
	}
	defer rd.Close()
	var out bytes.Buffer
	_, _ = stdcopy.StdCopy(&out, &out, rd)
	for _, l := range strings.Split(strings.ReplaceAll(out.String(), "\r\n", "\n"), "\n") {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		if r := []rune(l); len(r) > maxEvidenceLine {
			l = string(r[:maxEvidenceLine]) + "…"
		}
		lines = append(lines, l)
	}
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return summary, lines
}
