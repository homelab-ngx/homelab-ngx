package fixture

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"homelab-ngx.com/core/internal/imagecheck/vm"
)

// Run runs a host tool and returns what it printed; a failure carries what
// it said.
func Run(stdin string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// WinDisk makes path a stand-in for a Windows disk of 90 GB: an ESP (esp,
// such as 512M; Windows' own setup makes 100M) with Windows Boot Manager
// and a fallback loader of its own, then gap free (as Windows leaves after
// shrinking or deleting a partition; "" for none), an NTFS-typed partition
// of 20 GB, and the rest free. The loaders' files hold WINDOWS-BOOTMGR and
// WINDOWS-FALLBACK.
func WinDisk(path, gap, esp string) error {
	if err := vm.CreateRaw(path, 90<<30); err != nil {
		return err
	}
	script := fmt.Sprintf("label: gpt\n,%s,U\n,20G,%s\n", esp, TypeNTFS)
	if gap != "" {
		script = fmt.Sprintf("label: gpt\n,%s,U\n,%s,L\n,20G,%s\n", esp, gap, TypeNTFS)
	}
	if _, err := Run(script, "sfdisk", "-q", path); err != nil {
		return err
	}
	if gap != "" {
		if _, err := Run("", "sfdisk", "-q", "--delete", path, "2"); err != nil {
			return err
		}
	}
	t, err := ReadGPT(path)
	if err != nil {
		return err
	}
	ps := t.ByType(TypeESP)
	if len(ps) != 1 {
		return fmt.Errorf("%s: no ESP: %s", path, t)
	}
	dir, err := os.MkdirTemp(filepath.Dir(path), "esp")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	img := filepath.Join(dir, "esp.img")
	if err := os.Truncate(img, int64(ps[0].Size*512)); err != nil {
		f, cerr := os.Create(img)
		if cerr != nil {
			return cerr
		}
		f.Close()
		if err := os.Truncate(img, int64(ps[0].Size*512)); err != nil {
			return err
		}
	}
	if _, err := Run("", "mkfs.vfat", "-F", "32", "-n", "EFI", img); err != nil {
		return err
	}
	files := map[string]string{"bootmgfw.efi": "WINDOWS-BOOTMGR\n", "fallback.efi": "WINDOWS-FALLBACK\n"}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			return err
		}
	}
	if _, err := Run("", "mmd", "-i", img, "::/EFI", "::/EFI/BOOT", "::/EFI/Microsoft", "::/EFI/Microsoft/Boot"); err != nil {
		return err
	}
	if _, err := Run("", "mcopy", "-i", img, filepath.Join(dir, "fallback.efi"), "::/EFI/BOOT/BOOTX64.EFI"); err != nil {
		return err
	}
	if _, err := Run("", "mcopy", "-i", img, filepath.Join(dir, "bootmgfw.efi"), "::/EFI/Microsoft/Boot/bootmgfw.efi"); err != nil {
		return err
	}
	return CopyAt(img, 0, path, int64(ps[0].Start*512), int64(ps[0].Size*512))
}

// CopyAt copies n bytes of src from offset from into dst at offset to,
// leaving runs of zeros as holes.
func CopyAt(src string, from int64, dst string, to int64, n int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 1<<20)
	zero := make([]byte, len(buf))
	for done := int64(0); done < n; {
		k := int64(len(buf))
		if n-done < k {
			k = n - done
		}
		m, err := in.ReadAt(buf[:k], from+done)
		if m == 0 && err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if !bytes.Equal(buf[:m], zero[:m]) {
			if _, err := out.WriteAt(buf[:m], to+done); err != nil {
				return err
			}
		}
		done += int64(m)
	}
	return out.Close()
}

// Extract copies sectors [start, start+count) of a disk file (any format)
// to a raw file.
func Extract(disk string, start, count uint64, out string) error {
	f := "raw"
	if strings.HasSuffix(disk, ".qcow2") {
		f = "qcow2"
	}
	_ = os.Remove(out)
	return vm.QemuImg("dd", "-U", "-f", f, "-O", "raw", "bs=512",
		fmt.Sprintf("skip=%d", start), fmt.Sprintf("count=%d", start+count),
		"if="+disk, "of="+out)
}

// ESPDisk makes out a disk with one ESP holding a copy of disk's ESP, so
// that the firmware can start a system whose own disk it cannot read.
func ESPDisk(disk, out string) error {
	t, err := ReadGPT(disk)
	if err != nil {
		return err
	}
	ps := t.ByType(TypeESP)
	if len(ps) != 1 {
		return fmt.Errorf("%s has %d ESPs: %s", disk, len(ps), t)
	}
	esp := ps[0]
	if err := vm.CreateRaw(out, int64((esp.Size+4096)*512)); err != nil {
		return err
	}
	if _, err := Run(fmt.Sprintf("label: gpt\n2048,%d,U\n", esp.Size), "sfdisk", "-q", out); err != nil {
		return err
	}
	part := out + ".part"
	defer os.Remove(part)
	if err := Extract(disk, esp.Start, esp.Size, part); err != nil {
		return err
	}
	return CopyAt(part, 0, out, 2048*512, int64(esp.Size*512))
}
