// Package updater replaces a running DeployMate with a newer release: fetch and
// verify the archive, swap the binary, restart the service, and put the old
// version back if the new one does not come up.
package updater

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

// ErrNotInArchive is returned by Extract for an entry the archive lacks.
var ErrNotInArchive = errors.New("not in the archive")

// Release names where a version's files live.
type Release struct {
	Base    string // directory URL holding the archive and checksums.txt
	Archive string // deploymate_linux_amd64.tar.gz
}

// ArchiveName is the release archive for this machine.
func ArchiveName() string {
	return fmt.Sprintf("deploymate_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

// ResolveBase returns the download directory for a version ("latest" or a tag).
// baseOverride (DEPLOYMATE_BASE_URL) wins, for mirrors and tests.
func ResolveBase(repo, version, baseOverride string) string {
	switch {
	case baseOverride != "":
		return strings.TrimRight(baseOverride, "/")
	case version == "" || version == "latest":
		return "https://github.com/" + repo + "/releases/latest/download"
	default:
		return "https://github.com/" + repo + "/releases/download/" + version
	}
}

// LatestTag asks GitHub which tag "latest" points at, without the API (no rate
// limit, no token): the /releases/latest page redirects to /releases/tag/<tag>.
func LatestTag(ctx context.Context, repo string) (string, error) {
	c := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/"+repo+"/releases/latest", nil)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if i := strings.LastIndex(loc, "/tag/"); i >= 0 {
		return loc[i+len("/tag/"):], nil
	}
	return "", errors.New("could not find a published release (is the repository public and tagged?)")
}

// Fetch downloads url into dest.
func Fetch(ctx context.Context, url, dest string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// SHA256File returns the hex SHA-256 of a file.
func SHA256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyChecksum checks archive against its entry in a checksums.txt file
// ("<sha256>  <name>" lines). It refuses a mismatch and an unlisted archive.
func VerifyChecksum(archivePath, checksumsPath string) error {
	name := path.Base(archivePath)
	f, err := os.Open(checksumsPath)
	if err != nil {
		return err
	}
	defer f.Close()
	want := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			want = fields[0]
		}
	}
	if want == "" {
		return fmt.Errorf("%s is not listed in checksums.txt", name)
	}
	got, err := SHA256File(archivePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", name, want, got)
	}
	return nil
}

// Extract reads only the named entries from a .tar.gz into outDir (flat, by
// base name). Nothing else in the archive is written, so a crafted path can't
// land anywhere.
func Extract(archivePath, outDir string, entries map[string]string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	found := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		out, ok := entries[path.Clean(h.Name)]
		if !ok || h.Typeflag != tar.TypeReg {
			continue
		}
		dst, err := os.OpenFile(path.Join(outDir, out), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(dst, io.LimitReader(tr, 512<<20)); err != nil {
			dst.Close()
			return err
		}
		if err := dst.Close(); err != nil {
			return err
		}
		found[out] = true
	}
	for _, out := range entries {
		if !found[out] {
			return fmt.Errorf("%w: %s", ErrNotInArchive, out)
		}
	}
	return nil
}
