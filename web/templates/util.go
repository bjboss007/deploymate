package templates

import (
	"strconv"
	"strings"
	"time"
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
