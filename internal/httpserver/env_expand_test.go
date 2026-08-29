package httpserver

import "testing"

func TestExpandRefs(t *testing.T) {
	env := map[string]string{
		"MYSQL_URL":  "mysql://dm:pw@host:3306/app",
		"DATABASE_URL": "${MYSQL_URL}",
		"OTHER":      "prefix-${MYSQL_URL}-suffix",
		"CHAIN_A":    "${CHAIN_B}",
		"CHAIN_B":    "resolved",
		"PLAIN":      "no refs",
		"TYPOD":      "${DOES_NOT_EXIST}",
	}
	out := expandRefs(env, 5)
	want := map[string]string{
		"MYSQL_URL":  "mysql://dm:pw@host:3306/app",
		"DATABASE_URL": "mysql://dm:pw@host:3306/app",
		"OTHER":      "prefix-mysql://dm:pw@host:3306/app-suffix",
		"CHAIN_A":    "resolved",
		"CHAIN_B":    "resolved",
		"PLAIN":      "no refs",
		"TYPOD":      "${DOES_NOT_EXIST}", // left as-is, visible
	}
	for k, w := range want {
		if out[k] != w {
			t.Errorf("%s = %q, want %q", k, out[k], w)
		}
	}
}

func TestExpandRefsCycleBounded(t *testing.T) {
	env := map[string]string{
		"A": "${B}",
		"B": "${A}",
	}
	out := expandRefs(env, 5)
	if out["A"] != "${B}" && out["A"] != "${A}" {
		t.Fatalf("cycle must terminate with unresolved refs, got %q", out["A"])
	}
}
