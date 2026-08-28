package templates

import (
	"strconv"
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
