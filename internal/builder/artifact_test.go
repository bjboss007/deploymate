package builder

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zipEntry struct {
	name string
	body string
	mode os.FileMode // 0 = regular file
}

func makeZip(t *testing.T, entries ...zipEntry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(e.body))
	}
	zw.Close()
	p := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func extractTo(t *testing.T, zipPath string, max int64) (string, string, int64, error) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "app.jar")
	name, n, err := ExtractJar(zipPath, dest, max)
	return dest, name, n, err
}

func TestExtractJarSingle(t *testing.T) {
	for _, name := range []string{"app.jar", "build/libs/service-1.0.jar", "App.JAR"} {
		z := makeZip(t, zipEntry{name: name, body: "JARBYTES"})
		dest, got, n, err := extractTo(t, z, 1<<20)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := os.ReadFile(dest)
		if string(b) != "JARBYTES" || n != 8 || got != filepath.Base(name) {
			t.Errorf("%s: content %q n=%d name=%q", name, b, n, got)
		}
	}
}

// TestExtractJarSkipsPlainAndNonJars: Spring's "-plain.jar" companion, other
// files, directories, and symlinks are not candidates.
func TestExtractJarSkipsPlainAndNonJars(t *testing.T) {
	z := makeZip(t,
		zipEntry{name: "app-plain.jar", body: "PLAIN"},
		zipEntry{name: "README.txt", body: "x"},
		zipEntry{name: "lib.jar/", body: ""},                                  // a directory named like a jar
		zipEntry{name: "link.jar", body: "/etc/passwd", mode: os.ModeSymlink}, // a symlink entry
		zipEntry{name: "app.jar", body: "THE-APP"},
	)
	dest, name, _, err := extractTo(t, z, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "THE-APP" || name != "app.jar" {
		t.Errorf("picked %q (%q), want app.jar", name, b)
	}
}

func TestExtractJarAmbiguousOrMissing(t *testing.T) {
	_, _, _, err := extractTo(t, makeZip(t, zipEntry{name: "a.jar", body: "1"}, zipEntry{name: "sub/b.jar", body: "2"}), 1<<20)
	if err == nil || !strings.Contains(err.Error(), "exactly one .jar") || !strings.Contains(err.Error(), "a.jar, b.jar") {
		t.Errorf("two jars err = %v, want a message listing both", err)
	}
	_, _, _, err = extractTo(t, makeZip(t, zipEntry{name: "notes.txt", body: "x"}, zipEntry{name: "app-plain.jar", body: "p"}), 1<<20)
	if err == nil || !strings.Contains(err.Error(), "no .jar found") || !strings.Contains(err.Error(), "notes.txt") {
		t.Errorf("no jar err = %v, want a message listing what is inside", err)
	}
}

// TestExtractJarZipSlip: a hostile entry name never influences where bytes
// land — the destination is only what the caller passed.
func TestExtractJarZipSlip(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "evil.jar")
	z := makeZip(t, zipEntry{name: "../../" + filepath.Base(root) + "/evil.jar", body: "PWN"})
	dest := filepath.Join(root, "work", "app.jar")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	name, _, err := ExtractJar(z, dest, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if name != "evil.jar" {
		t.Errorf("name = %q", name)
	}
	if _, err := os.Stat(victim); err == nil {
		t.Error("zip-slip: a file was written outside the destination")
	}
	if b, _ := os.ReadFile(dest); string(b) != "PWN" {
		t.Errorf("bytes must land at the caller's destination, got %q", b)
	}
}

func TestExtractJarLimitsAndCorruption(t *testing.T) {
	big := strings.Repeat("A", 4096)
	if _, _, _, err := extractTo(t, makeZip(t, zipEntry{name: "app.jar", body: big}), 1024); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("oversize jar err = %v", err)
	}
	// Not a zip at all.
	p := filepath.Join(t.TempDir(), "x.zip")
	os.WriteFile(p, []byte("this is not a zip"), 0o644)
	if _, _, _, err := extractTo(t, p, 1<<20); err == nil || !strings.Contains(err.Error(), "not a valid zip") {
		t.Errorf("corrupt zip err = %v", err)
	}
	// Too many entries.
	var entries []zipEntry
	for i := 0; i <= maxZipEntries; i++ {
		entries = append(entries, zipEntry{name: "f" + string(rune('a'+i%26)) + strings.Repeat("x", i%7) + "/" + string(rune('a'+i/26%26)) + ".txt", body: "x"})
	}
	if _, _, _, err := extractTo(t, makeZip(t, entries...), 1<<20); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Errorf("entry-count err = %v", err)
	}
	// The destination is never overwritten.
	dest := filepath.Join(t.TempDir(), "app.jar")
	os.WriteFile(dest, []byte("existing"), 0o644)
	if _, _, err := ExtractJar(makeZip(t, zipEntry{name: "app.jar", body: "new"}), dest, 1<<20); err == nil {
		t.Error("ExtractJar must refuse to overwrite an existing destination (O_EXCL)")
	}
}

func TestJavaMajor(t *testing.T) {
	for in, want := range map[string]string{
		"":            "21",
		"java":        "21",
		"java:21":     "21",
		"java:17":     "17",
		"java:21.0.2": "21",
		"java:8":      "8",
		"node:22":     "21", // not a Java runtime → default
		"java:abc":    "21",
		"java:123":    "21", // 3 digits never reach a FROM line
	} {
		if got := JavaMajor(in); got != want {
			t.Errorf("JavaMajor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJavaWrapperDockerfile(t *testing.T) {
	df, err := JavaWrapperDockerfile("17")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM eclipse-temurin:17-jre",
		`ENV JAVA_OPTS="-XX:MaxRAMPercentage=75"`,
		"USER app",
		"COPY --chown=app app.jar /app/app.jar",
		"-Dserver.port=${PORT:-8080}",
		"exec java $JAVA_OPTS",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile missing %q:\n%s", want, df)
		}
	}
	for _, bad := range []string{"", "21 ", "latest", "21\nRUN evil", "1.2", "123", "../x"} {
		if _, err := JavaWrapperDockerfile(bad); err == nil {
			t.Errorf("JavaWrapperDockerfile(%q) must be rejected", bad)
		}
	}
}
