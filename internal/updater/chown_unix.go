package updater

import (
	"os"
	"syscall"
)

// chownLike gives dst the owner of ref, so the service user can read a backup
// made by root.
func chownLike(dst, ref string) error {
	fi, err := os.Stat(ref)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return os.Chown(dst, int(st.Uid), int(st.Gid))
	}
	return nil
}
