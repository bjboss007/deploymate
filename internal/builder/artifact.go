package builder

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Limits for prebuilt artifacts (docs/specs/prebuilt-deploys.md). The zip cap
// is enforced by the downloader; these bound what is read out of it.
const (
	// MaxArtifactZipBytes caps the downloaded artifact archive.
	MaxArtifactZipBytes int64 = 400 << 20
	// MaxJarBytes caps the single JAR extracted from it.
	MaxJarBytes int64 = 400 << 20
	// maxZipEntries bounds how many entries we are willing to look at.
	maxZipEntries = 1000
)

// ExtractJar copies the ONE application JAR out of an artifact zip to
// destPath and returns its name inside the zip and its size.
//
// Safety: the destination is chosen by the caller — a name from inside the
// archive is never used to build a path, so zip-slip has nowhere to land.
// Directories and symlink entries are ignored (never followed), spring's
// "-plain.jar" companion is skipped, and the copy is capped so a crafted
// zip cannot expand without bound. Zero or several candidate JARs is an
// error that names them — DeployMate never guesses which one is the app.
func ExtractJar(zipPath, destPath string, maxJarBytes int64) (string, int64, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", 0, fmt.Errorf("the artifact is not a valid zip archive: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > maxZipEntries {
		return "", 0, fmt.Errorf("the artifact has %d entries (limit %d) — upload just the one JAR", len(zr.File), maxZipEntries)
	}

	var jars []*zip.File
	var names []string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		base := path.Base(strings.ReplaceAll(f.Name, "\\", "/"))
		names = append(names, base)
		low := strings.ToLower(base)
		if strings.HasSuffix(low, ".jar") && !strings.HasSuffix(low, "-plain.jar") {
			jars = append(jars, f)
		}
	}
	switch {
	case len(jars) == 0:
		return "", 0, fmt.Errorf("no .jar found in the artifact (it contains: %s)", listNames(names))
	case len(jars) > 1:
		var found []string
		for _, f := range jars {
			found = append(found, path.Base(strings.ReplaceAll(f.Name, "\\", "/")))
		}
		return "", 0, fmt.Errorf("expected exactly one .jar in the artifact, found: %s", listNames(found))
	}

	f := jars[0]
	if f.UncompressedSize64 > uint64(maxJarBytes) {
		return "", 0, fmt.Errorf("the jar is %d MB uncompressed, over the %d MB limit", f.UncompressedSize64>>20, maxJarBytes>>20)
	}
	rc, err := f.Open()
	if err != nil {
		return "", 0, fmt.Errorf("open %s in the artifact: %w", path.Base(f.Name), err)
	}
	defer rc.Close()
	out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", 0, err
	}
	n, copyErr := io.Copy(out, io.LimitReader(rc, maxJarBytes+1))
	closeErr := out.Close()
	switch {
	case copyErr != nil: // includes zip.ErrChecksum from a corrupt entry
		os.Remove(destPath)
		return "", 0, fmt.Errorf("read %s from the artifact: %w", path.Base(f.Name), copyErr)
	case n > maxJarBytes:
		os.Remove(destPath)
		return "", 0, fmt.Errorf("the jar is over the %d MB limit", maxJarBytes>>20)
	case closeErr != nil:
		os.Remove(destPath)
		return "", 0, closeErr
	}
	return path.Base(strings.ReplaceAll(f.Name, "\\", "/")), n, nil
}

// listNames renders up to ten names, sorted, for error messages.
func listNames(names []string) string {
	sort.Strings(names)
	if len(names) > 10 {
		return strings.Join(names[:10], ", ") + fmt.Sprintf(", … (%d more)", len(names)-10)
	}
	if len(names) == 0 {
		return "nothing"
	}
	return strings.Join(names, ", ")
}

var majorRe = regexp.MustCompile(`^[0-9]{1,2}$`)

// DefaultJavaMajor is the JRE the wrapper uses when the app has no Java
// runtime version set.
const DefaultJavaMajor = "21"

// JavaMajor derives the JRE major version from an app's runtime setting
// ("java:21", "java:17", "java:21.0.2", "java", ""): the first dotted
// component, defaulting to DefaultJavaMajor. Anything else is also the
// default — the result goes into a FROM line, so it is only ever 1–2 digits.
func JavaMajor(runtime string) string {
	spec := ParseRuntimeSpec(runtime)
	if spec.Key != "java" || spec.Version == "" {
		return DefaultJavaMajor
	}
	major := strings.SplitN(spec.Version, ".", 2)[0]
	if !majorRe.MatchString(major) {
		return DefaultJavaMajor
	}
	return major
}

// ErrBadJavaVersion rejects a Java major version that is not 1–2 digits.
var ErrBadJavaVersion = errors.New("builder: invalid Java version")

// JavaWrapperDockerfile renders the Dockerfile that wraps a prebuilt JAR
// (app.jar next to it) in an official Temurin JRE image: non-root, honors
// the platform's PORT and an overridable JAVA_OPTS (default: use 75% of the
// container's memory limit for the heap). Measured in spike S2: 1.4 s to
// build, no measurable extra memory.
func JavaWrapperDockerfile(javaMajor string) (string, error) {
	if !majorRe.MatchString(javaMajor) {
		return "", ErrBadJavaVersion
	}
	return `FROM eclipse-temurin:` + javaMajor + `-jre
ENV JAVA_OPTS="-XX:MaxRAMPercentage=75"
RUN useradd -r -u 10001 app
COPY --chown=app app.jar /app/app.jar
USER app
ENTRYPOINT ["sh", "-c", "exec java $JAVA_OPTS -Dserver.port=${PORT:-8080} -jar /app/app.jar"]
`, nil
}
