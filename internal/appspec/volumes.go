package appspec

import (
	"fmt"
	"hash/crc32"
	"strings"
)

// MaxVolumes bounds the persistent folders of one app.
const MaxVolumes = 8

// reservedMounts are places a volume must never cover: they hold the container's own runtime state.
var reservedMounts = []string{"/proc", "/sys", "/dev", "/run", "/var/run", "/etc/resolv.conf", "/etc/hosts", "/etc/hostname"}

// ParseVolumes turns the owner's text (one path per line, blanks ignored) into clean absolute paths, or says what is wrong.
func ParseVolumes(raw string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("%q is not an absolute path (it must start with /)", p)
		}
		if strings.ContainsAny(p, ":, \t\\\x00") || strings.Contains(p, "..") {
			return nil, fmt.Errorf("%q has characters a folder path cannot have (spaces, colons, commas, ..)", p)
		}
		p = "/" + strings.Trim(p, "/")
		if p == "/" {
			return nil, fmt.Errorf("the root folder / cannot be a persistent folder")
		}
		for _, r := range reservedMounts {
			if p == r || strings.HasPrefix(p, r+"/") {
				return nil, fmt.Errorf("%s is used by the container itself", p)
			}
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) > MaxVolumes {
		return nil, fmt.Errorf("at most %d persistent folders", MaxVolumes)
	}
	return out, nil
}

// VolumeName is the named Docker volume behind one of an app's folders. It survives deploys and deleting the app,
// like a database's volume.
func VolumeName(slug, path string) string {
	return fmt.Sprintf("dm-app-%s-%08x", slug, crc32.ChecksumIEEE([]byte(path)))
}

// volumeBinds are the Docker mounts for the app's persistent folders.
func volumeBinds(slug, raw string) []string {
	paths, _ := ParseVolumes(raw)
	binds := make([]string, 0, len(paths))
	for _, p := range paths {
		binds = append(binds, VolumeName(slug, p)+":"+p)
	}
	return binds
}
