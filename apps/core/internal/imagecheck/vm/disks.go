package vm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// QemuImg runs qemu-img and returns what it said when it fails.
func QemuImg(args ...string) error {
	out, err := exec.Command("qemu-img", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("qemu-img %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CreateRaw makes an empty sparse raw disk of size bytes.
func CreateRaw(path string, size int64) error {
	return QemuImg("create", "-q", "-f", "raw", path, strconv.FormatInt(size, 10))
}

// Overlay makes a qcow2 disk at path whose unwritten blocks are base's.
// base is not written again while the overlay exists.
func Overlay(base, path string) error {
	f := "raw"
	if strings.HasSuffix(base, ".qcow2") {
		f = "qcow2"
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	return QemuImg("create", "-q", "-f", "qcow2", "-F", f, "-b", abs, path)
}

// Used is how many bytes the files under dir take on disk (their blocks,
// not their sizes: the disks are sparse).
func Used(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			n += st.Blocks * 512
		} else {
			n += info.Size()
		}
		return nil
	})
	return n
}
