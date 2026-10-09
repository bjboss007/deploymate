package templates

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
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

// ago renders an RFC3339Nano timestamp relative to now ("just now", "5 min
// ago", "3 h ago", "2 d ago"); older than 30 days falls back to the date.
func ago(ts string) string { return agoAt(ts, time.Now()) }

func agoAt(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < 45*time.Second:
		return "just now"
	case d < 90*time.Minute && d >= time.Minute:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < time.Minute:
		return "1 min ago"
	case d < 36*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d d ago", int(d.Hours()/24))
	}
	return t.Local().Format("2006-01-02")
}

// clip shortens s to at most n runes, adding an ellipsis.
func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// currentDeployment finds the deployment the app is serving among the recent
// ones; ok is false when it is not in the list (e.g. never deployed).
func currentDeployment(app store.App, ds []store.Deployment) (store.Deployment, bool) {
	for _, d := range ds {
		if d.ID == app.CurrentDeploymentID && d.ID != "" {
			return d, true
		}
	}
	return store.Deployment{}, false
}

// recent returns at most n deployments (the list is newest first).
func recent(ds []store.Deployment, n int) []store.Deployment {
	if len(ds) > n {
		return ds[:n]
	}
	return ds
}

// versionLabel is what a deployment shipped, in a few characters: the image
// tag, else the short commit, else its kind.
func versionLabel(d store.Deployment) string {
	switch {
	case d.ImageTag != "":
		return clip(d.ImageTag, 40)
	case d.CommitSHA != "":
		return shortDeployID(d.CommitSHA)
	}
	return d.Kind
}

// pctNum renders a percentage as a bare number (for aria-valuenow).
func pctNum(v float64) string { return strconv.FormatFloat(v, 'f', 0, 64) }

// meterWidth is the inline width of a meter bar, clamped to 0-100%.
func meterWidth(pct float64) templ.SafeCSS {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return templ.SafeCSS("width:" + strconv.FormatFloat(pct, 'f', 1, 64) + "%")
}
