package httpserver

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/crypto"
)

func TestParseEnvLines(t *testing.T) {
	got, errs := parseEnvLines("# database\r\nDB_URL=jdbc:postgresql://h:5432/app?x=1&y=2\n\nexport DB_USER = dm \nDB_PASSWORD=\"pa ss=word\"\nNAME='quoted'\nEMPTY=\nDB_USER=last-wins\n   # indented comment\nSPACED=a b  c  \n")
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	want := []envKV{
		{"DB_URL", "jdbc:postgresql://h:5432/app?x=1&y=2"}, // '=' inside the value survives
		{"DB_USER", "last-wins"},                           // duplicate keeps the last value, first position
		{"DB_PASSWORD", "pa ss=word"},
		{"NAME", "quoted"},
		{"EMPTY", ""},
		{"SPACED", "a b  c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

// Nothing is returned when any line is bad, and the errors name the lines.
func TestParseEnvLinesRejectsBadInputAtomically(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"GOOD=1\nnot a pair\n", "line 2: expected KEY=VALUE"},
		{"1BAD=x\n", `line 1: "1BAD" is not a valid name`},
		{"has space=x\n", `"has space" is not a valid name`},
		{"=novalue\n", "is not a valid name"},
		{"K=" + strings.Repeat("a", maxEnvValueLen+1), "too long"},
	} {
		got, errs := parseEnvLines(tc.in)
		if got != nil || len(errs) == 0 || !strings.Contains(strings.Join(errs, "|"), tc.want) {
			t.Errorf("parseEnvLines(%q) = %v, %v; want nil and an error containing %q", tc.in, got, errs, tc.want)
		}
	}
	var b strings.Builder
	for i := 0; i <= maxBulkEnvLines; i++ {
		b.WriteString("K" + strings.Repeat("x", i%7) + "=1\n")
	}
	if got, errs := parseEnvLines(b.String()); got != nil || len(errs) == 0 {
		t.Errorf("a paste over %d lines must be refused", maxBulkEnvLines)
	}
}

func TestLooksSecret(t *testing.T) {
	for _, k := range []string{"DB_PASSWORD", "JWT_SECRET", "GITHUB_TOKEN", "R2_SECRET_KEY", "R2_ACCESS_KEY", "API_KEY", "ENCRYPTION_KEY_VERSION", "TRADESTACK_ENCRYPTION_KEY", "STRIPE_KEY", "mail_password"} {
		if !looksSecret(k) {
			t.Errorf("%s should be treated as secret", k)
		}
	}
	for _, k := range []string{"DB_URL", "DB_USER", "REDIS_HOST", "PORT", "PUBLIC_BASE_URL", "STORAGE_PROVIDER", "KEYCLOAK_URL"} {
		if looksSecret(k) {
			t.Errorf("%s should not be treated as secret", k)
		}
	}
}

func TestParseEnvRows(t *testing.T) {
	form := url.Values{
		"key_0": {"DB_URL"}, "value_0": {"jdbc:x"},
		"key_1": {""}, "value_1": {""}, // the untouched empty row is skipped
		"key_2": {" DB_PASSWORD "}, "value_2": {"pw"}, "secret_2": {"on"},
		"key_10": {"LATER"}, "value_10": {"v"}, // numeric, not lexical, order: 2 before 10
	}
	got, errs := parseEnvRows(form)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	want := []envItem{{"DB_URL", "jdbc:x", false}, {"DB_PASSWORD", "pw", true}, {"LATER", "v", false}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	_, errs = parseEnvRows(url.Values{"key_0": {"OK"}, "value_0": {"1"}, "key_1": {"bad name"}, "value_1": {"x"}, "key_2": {""}, "value_2": {"orphan"}})
	if len(errs) != 2 || !strings.Contains(errs[0], "row 2") || !strings.Contains(errs[1], "row 3: the name is empty") {
		t.Errorf("errs = %v", errs)
	}
}

func TestEnvBulkHandler(t *testing.T) {
	e := newPrebuiltEnv(t)
	vars := func() map[string]string {
		list, _ := e.st.ListEnvVars(e.app.ID)
		out := map[string]string{}
		for _, v := range list {
			plain, err := crypto.Decrypt(e.s.encKey, v.ValueEnc)
			if err != nil {
				t.Fatal(err)
			}
			mark := ""
			if v.IsSecret {
				mark = " [secret]"
			}
			out[v.Key] = plain + mark
		}
		return out
	}
	save := func(f url.Values) string {
		_, loc := e.post(t, "/apps/api/env/bulk", f)
		return flashOf(t, loc)
	}

	// Several rows + an existing key, saved with one request.
	e.post(t, "/apps/api/env", url.Values{"key": {"DB_USER"}, "value": {"old"}})
	f := save(url.Values{
		"key_0": {"DB_URL"}, "value_0": {"jdbc:postgresql://h:5432/app"},
		"key_1": {"DB_USER"}, "value_1": {"dm"},
		"key_2": {"DB_PASSWORD"}, "value_2": {"s3cret"},
		"key_3": {"REDIS_HOST"}, "value_3": {"r"}, "secret_3": {"on"},
		"key_4": {""}, "value_4": {""},
	})
	if !strings.Contains(f, "Saved 4 variable(s) (3 new, 1 updated)") {
		t.Errorf("flash = %q", f)
	}
	want := map[string]string{
		"DB_URL": "jdbc:postgresql://h:5432/app", "DB_USER": "dm", "DB_PASSWORD": "s3cret [secret]", "REDIS_HOST": "r [secret]",
	}
	if got := vars(); !reflect.DeepEqual(got, want) {
		t.Errorf("vars = %v, want %v", got, want)
	}

	// Rows and a pasted block save together; "mark all pasted as secret".
	save(url.Values{"key_0": {"ROW"}, "value_0": {"1"}, "bulk": {"PASTED=2\nDB_URL=changed"}, "is_secret": {"on"}})
	got := vars()
	if got["ROW"] != "1" || got["PASTED"] != "2 [secret]" || got["DB_URL"] != "changed [secret]" {
		t.Errorf("rows+paste = %v", got)
	}

	// A bad row rejects the whole save: nothing is written.
	before := len(vars())
	f = save(url.Values{"key_0": {"FINE"}, "value_0": {"1"}, "key_1": {"bad name"}, "value_1": {"x"}})
	if !strings.Contains(f, "Nothing saved") || !strings.Contains(f, "row 2") {
		t.Errorf("flash = %q", f)
	}
	if got := vars(); len(got) != before || got["FINE"] != "" {
		t.Errorf("a rejected save still wrote variables: %v", got)
	}
	// ... and so does a bad pasted line, even when the rows are fine.
	f = save(url.Values{"key_0": {"FINE2"}, "value_0": {"1"}, "bulk": {"broken line"}})
	if !strings.Contains(f, "Nothing saved") || vars()["FINE2"] != "" {
		t.Errorf("bad paste: flash %q vars %v", f, vars())
	}

	// Nothing to save.
	if f := save(url.Values{"key_0": {""}, "value_0": {""}}); !strings.Contains(f, "at least one variable") {
		t.Errorf("flash = %q", f)
	}

	// Values never reach the event log (only names).
	evs, _ := e.st.ListEvents(e.app.ID, 20)
	for _, ev := range evs {
		if strings.Contains(ev.Data, "s3cret") {
			t.Errorf("a secret value leaked into an event: %q", ev.Data)
		}
	}
}
