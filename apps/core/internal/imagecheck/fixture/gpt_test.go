package fixture

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// putGUID writes a GUID's text form in GPT's mixed-endian layout.
func putGUID(b []byte, a uint32, c, d uint16, e [8]byte) {
	binary.LittleEndian.PutUint32(b[0:], a)
	binary.LittleEndian.PutUint16(b[4:], c)
	binary.LittleEndian.PutUint16(b[6:], d)
	copy(b[8:], e[:])
}

func TestReadGPT(t *testing.T) {
	disk := make([]byte, 64*512)
	h := disk[512:]
	copy(h, "EFI PART")
	putGUID(h[56:], 0x11223344, 0x5566, 0x7788, [8]byte{0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00})
	binary.LittleEndian.PutUint64(h[72:], 2)
	binary.LittleEndian.PutUint32(h[80:], 128)
	binary.LittleEndian.PutUint32(h[84:], 128)
	e := disk[2*512+128:] // the second entry; the first is empty
	// C12A7328-F81F-11D2-BA4B-00A0C93EC93B, the ESP.
	putGUID(e[0:], 0xC12A7328, 0xF81F, 0x11D2, [8]byte{0xBA, 0x4B, 0x00, 0xA0, 0xC9, 0x3E, 0xC9, 0x3B})
	putGUID(e[16:], 0xABCDEF01, 0x2345, 0x6789, [8]byte{1, 2, 3, 4, 5, 6, 7, 8})
	binary.LittleEndian.PutUint64(e[32:], 2048)
	binary.LittleEndian.PutUint64(e[40:], 4095)
	for i, u := range utf16.Encode([]rune("esp")) {
		binary.LittleEndian.PutUint16(e[56+2*i:], u)
	}
	p := filepath.Join(t.TempDir(), "d.raw")
	if err := os.WriteFile(p, disk, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGPT(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.DiskID != "11223344-5566-7788-99aa-bbccddeeff00" {
		t.Errorf("disk id %s", got.DiskID)
	}
	want := Partition{N: 2, Type: TypeESP, UUID: "abcdef01-2345-6789-0102-030405060708", Name: "esp", Start: 2048, Size: 2048}
	if len(got.Partitions) != 1 || got.Partitions[0] != want {
		t.Errorf("partitions %+v, want %+v", got.Partitions, want)
	}
	if len(got.ByType(TypeESP)) != 1 || len(got.ByName("esp")) != 1 || len(got.ByName("var")) != 0 {
		t.Errorf("ByType/ByName: %s", got)
	}
}

func TestReadGPTNone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.raw")
	if err := os.WriteFile(p, make([]byte, 64*512), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGPT(p); err == nil {
		t.Error("a disk without a GPT read as one")
	}
}
