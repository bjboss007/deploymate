package httpserver

import "regexp"

var refRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandRefs resolves ${KEY} references in env values against the full
// env map, with chained expansion up to maxPasses (a bounded cycle
// guard). Unresolved references are left as-is — a typo must be visible
// in the container env and logs, not silently drop the variable.
func expandRefs(env map[string]string, maxPasses int) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	for i := 0; i < maxPasses; i++ {
		changed := false
		for k, v := range out {
			next := refRe.ReplaceAllStringFunc(v, func(m string) string {
				key := m[2 : len(m)-1]
				if val, ok := out[key]; ok {
					changed = true
					return val
				}
				return m
			})
			out[k] = next
		}
		if !changed {
			break
		}
	}
	return out
}
