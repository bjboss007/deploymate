package templates

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// fmtPort renders a port as a string, empty when unset.
func fmtPort(p int) string {
	if p <= 0 {
		return ""
	}
	return strconv.Itoa(p)
}

// shortTime renders an RFC3339Nano timestamp as a compact local time.
func shortTime(ts string) string {
	if ts == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2006-01-02 15:04")
}

// shortDeployID shortens a deployment UUID for breadcrumbs.
func shortDeployID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// fmtPct renders a percentage compactly (99.75 → "99.8%").
func fmtPct(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64) + "%"
}

// fmtSec renders seconds as a compact duration.
func fmtSec(v float64) string {
	return (time.Duration(v) * time.Second).Round(time.Second).String()
}

// fmtMin renders minutes compactly.
func fmtMin(v float64) string {
	return (time.Duration(v) * time.Minute).Round(time.Minute).String()
}

// fmtCPU renders a float CPU core count compactly (1.5 → "1.5").
func fmtCPU(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// fmtBytes renders a byte count in human units ("1.2 GB"). Below 1 KiB it
// stays exact bytes; the size string is what the storage panel shows.
func fmtBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatUint(n, 10) + " B"
	}
	div, exp := unit, 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// pctOf renders a fraction as a percentage.
func pctOf(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// jsonForDayCounts renders day/count pairs as a JSON array for the fleet
// chart (plain ASCII, so script-tag embedding is safe).
func jsonForDayCounts(days []store.DayCount) string {
	if len(days) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, d := range days {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"day":%q,"count":%d}`, d.Day, d.Count)
	}
	b.WriteByte(']')
	return b.String()
}

// runtimeKey splits "node:22" → "node".
func runtimeKey(spec string) string {
	if i := strings.Index(spec, ":"); i >= 0 {
		return spec[:i]
	}
	return spec
}

// runtimeVersion splits "node:22" → "22".
func runtimeVersion(spec string) string {
	if i := strings.Index(spec, ":"); i >= 0 {
		return spec[i+1:]
	}
	return ""
}
