// Package fixture makes and reads what the image check's machines are given:
// disk files and their partition tables, and a release server.
package fixture

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"homelab-ngx.com/core/internal/imagecheck/vm"
)

// Partition types the check looks for.
const (
	TypeESP  = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"
	TypeNTFS = "EBD0A0A2-B9E5-4433-87C0-68B6B72699C7" // Microsoft basic data
)

// Partition is one entry of a GPT.
type Partition struct {
	N     int    // 1-based, as the kernel numbers it
	Type  string // upper case, as sfdisk prints it
	UUID  string // lower case
	Name  string
	Start uint64 // first sector (512 bytes)
	Size  uint64 // sectors
}

// Table is a disk's GPT.
type Table struct {
	DiskID     string // lower case
	Partitions []Partition
}

// ByType is the partitions of a type, in table order.
func (t Table) ByType(typ string) []Partition {
	var ps []Partition
	for _, p := range t.Partitions {
		if strings.EqualFold(p.Type, typ) {
			ps = append(ps, p)
		}
	}
	return ps
}

// ByName is the partitions with a name, in table order.
func (t Table) ByName(name string) []Partition {
	var ps []Partition
	for _, p := range t.Partitions {
		if p.Name == name {
			ps = append(ps, p)
		}
	}
	return ps
}

// String is the table in short, for a failure's message.
func (t Table) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "disk %s:", t.DiskID)
	for _, p := range t.Partitions {
		fmt.Fprintf(&b, " [%d %s %q start %d size %d]", p.N, p.Type, p.Name, p.Start, p.Size)
	}
	return b.String()
}

// ReadGPT reads a disk file's primary GPT: a raw file directly, any other
// format (qcow2) through qemu-img.
func ReadGPT(path string) (Table, error) {
	head, err := readSectors(path, 0, 34)
	if err != nil {
		return Table{}, err
	}
	t, entriesLBA, n, size, err := parseHeader(head)
	if err != nil {
		return Table{}, fmt.Errorf("%s: %w", path, err)
	}
	need := (uint64(n)*uint64(size) + 511) / 512
	ents := head
	if entriesLBA != 2 || 2+need > 34 {
		ents, err = readSectors(path, entriesLBA, need)
		if err != nil {
			return Table{}, err
		}
	} else {
		ents = head[2*512:]
	}
	t.Partitions = parseEntries(ents, n, size)
	return t, nil
}

func readSectors(path string, lba, count uint64) ([]byte, error) {
	if !strings.HasSuffix(path, ".qcow2") {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b := make([]byte, count*512)
		n, err := f.ReadAt(b, int64(lba*512))
		if n < len(b) {
			return nil, fmt.Errorf("read %s: %v", path, err)
		}
		return b, nil
	}
	dir, err := os.MkdirTemp("", "gpt")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "s.raw")
	if err := vm.QemuImg("dd", "-U", "-f", "qcow2", "-O", "raw", "bs=512",
		fmt.Sprintf("skip=%d", lba), fmt.Sprintf("count=%d", lba+count),
		"if="+path, "of="+out); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if uint64(len(b)) < count*512 {
		return nil, fmt.Errorf("read %s: %d bytes of %d", path, len(b), count*512)
	}
	return b[:count*512], nil
}

func parseHeader(b []byte) (t Table, entriesLBA uint64, n, size uint32, err error) {
	h := b[512:1024]
	if !bytes.Equal(h[:8], []byte("EFI PART")) {
		return t, 0, 0, 0, errors.New("no GPT")
	}
	t.DiskID = strings.ToLower(guid(h[56:72]))
	entriesLBA = binary.LittleEndian.Uint64(h[72:80])
	n = binary.LittleEndian.Uint32(h[80:84])
	size = binary.LittleEndian.Uint32(h[84:88])
	if size < 128 || n > 1024 {
		return t, 0, 0, 0, fmt.Errorf("odd GPT: %d entries of %d bytes", n, size)
	}
	return t, entriesLBA, n, size, nil
}

func parseEntries(b []byte, n, size uint32) []Partition {
	var ps []Partition
	for i := uint32(0); i < n && int((i+1)*size) <= len(b); i++ {
		e := b[i*size : (i+1)*size]
		if bytes.Equal(e[:16], make([]byte, 16)) {
			continue
		}
		first := binary.LittleEndian.Uint64(e[32:40])
		last := binary.LittleEndian.Uint64(e[40:48])
		u := make([]uint16, 36)
		for j := range u {
			u[j] = binary.LittleEndian.Uint16(e[56+2*j:])
		}
		name := string(utf16.Decode(u))
		if k := strings.IndexRune(name, 0); k >= 0 {
			name = name[:k]
		}
		ps = append(ps, Partition{
			N:     int(i) + 1,
			Type:  strings.ToUpper(guid(e[0:16])),
			UUID:  strings.ToLower(guid(e[16:32])),
			Name:  name,
			Start: first,
			Size:  last - first + 1,
		})
	}
	return ps
}

// guid is a GUID in its text form: the first three fields are little-endian.
func guid(b []byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x",
		binary.LittleEndian.Uint32(b[0:4]), binary.LittleEndian.Uint16(b[4:6]),
		binary.LittleEndian.Uint16(b[6:8]), b[8:10], b[10:16])
}
