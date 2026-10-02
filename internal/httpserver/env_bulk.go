package httpserver

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	maxBulkEnvLines = 200
	maxEnvValueLen  = 16 << 10
)

// envKV is one parsed KEY=VALUE pair.
type envKV struct{ Key, Value string }

// sensitiveKeyRe flags keys whose values should be stored masked: a pasted
// block usually mixes DB_USER with DB_PASSWORD, and the owner should not have
// to tick a box per line.
var sensitiveKeyRe = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE|CREDENTIAL|API_?KEY|ACCESS_?KEY|ENCRYPTION|_KEY$|^KEY$)`)

func looksSecret(key string) bool { return sensitiveKeyRe.MatchString(key) }

// parseEnvLines reads .env-style text: KEY=VALUE per line, blank lines and
// # comments ignored, an optional leading "export ", and one pair of
// surrounding single or double quotes stripped from the value (values may
// themselves contain "="). Unquoted values keep inner spaces but lose
// surrounding whitespace. A key repeated in the paste keeps its LAST value.
// Nothing is returned unless every line is valid, so a bad paste never
// half-saves; errs names the offending lines (1-based).
func parseEnvLines(text string) (vars []envKV, errs []string) {
	index := map[string]int{}
	n := 0
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n++
		if n > maxBulkEnvLines {
			errs = append(errs, fmt.Sprintf("more than %d variables in one paste", maxBulkEnvLines))
			break
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok {
			errs = append(errs, fmt.Sprintf("line %d: expected KEY=VALUE", i+1))
			continue
		}
		if !envKeyRe.MatchString(key) || len(key) > 128 {
			errs = append(errs, fmt.Sprintf("line %d: %q is not a valid name (letters, digits, underscores; must start with a letter or underscore)", i+1, truncate(key, 40)))
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
		if len(val) > maxEnvValueLen {
			errs = append(errs, fmt.Sprintf("line %d: the value of %s is too long", i+1, key))
			continue
		}
		if at, dup := index[key]; dup {
			vars[at].Value = val
			continue
		}
		index[key] = len(vars)
		vars = append(vars, envKV{Key: key, Value: val})
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return vars, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
