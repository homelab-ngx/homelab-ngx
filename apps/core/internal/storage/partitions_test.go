package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/store/storetest"
)

const (
	partCols  = "NAME,PATH,SIZE,TYPE,FSTYPE,UUID,LABEL,PARTTYPE,MODEL,MOUNTPOINTS"
	cacheUUID = "c2200313-0dba-4d75-a20d-abc99cf4076a"
	winUUID   = "01DA5C0A9E8B1230"
)

// partLsblk is a system disk (ESP, a root slot with its verity partition,
// swap, /var) with an ext4 partition beside them, a bcachefs disk, a LUKS
// disk, an NTFS disk and a whole-disk ext4. cacheAt is where the ext4
// partition is mounted, "" for nowhere.
func partLsblk(cacheAt string) string {
	mounts := func(m string) string {
		if m == "" {
			return "[null]"
		}
		return fmt.Sprintf("[%q]", m)
	}
	part := func(name, fstype, uuid, label, parttype, mount string) string {
		return fmt.Sprintf(`{"name":%q,"path":"/dev/%s","size":1000,"type":"part","fstype":%q,"uuid":%q,"label":%q,"parttype":%q,"model":null,"mountpoints":%s}`,
			name, name, fstype, uuid, label, parttype, mounts(mount))
	}
	disk := func(name, model, fstype, uuid, label string, children ...string) string {
		kids := ""
		if len(children) > 0 {
			kids = `,"children":[` + strings.Join(children, ",") + `]`
		}
		return fmt.Sprintf(`{"name":%q,"path":"/dev/%s","size":9000,"type":"disk","fstype":%q,"uuid":%q,"label":%q,"parttype":null,"model":%q,"mountpoints":[null]%s}`,
			name, name, fstype, uuid, label, model, kids)
	}
	return `{"blockdevices":[` + strings.Join([]string{
		disk("nvme0n1", "Samsung SSD 990 PRO 2TB", "", "", "",
			part("nvme0n1p1", "vfat", "AAAA-0001", "EFI", "c12a7328-f81f-11d2-ba4b-00a0c93ec93b", ""),
			part("nvme0n1p3", "ext4", cacheUUID, "cache", "0fc63daf-8483-4772-8e79-3d69d8477de4", cacheAt),
			part("nvme0n1p4", "erofs", "", "", "4f68bce3-e8cd-4db1-96e7-fbcaf984b709", ""),
			part("nvme0n1p5", "DM_verity_hash", "", "", "2c7357ed-ebd2-46d9-aec1-23d437ec2bf5", ""),
			part("nvme0n1p6", "swap", "swap-uuid", "", "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f", ""),
			part("nvme0n1p8", "ext4", "var-uuid", "var", "4d21b016-b534-45c2-a9fb-5c16e091fd2d", "/var"),
		),
		disk("sda", "ST4000", "bcachefs", poolUUID, ""),
		disk("sdb", "WD Red", "crypto_LUKS", "luks-uuid", ""),
		disk("sdc", "Elements", "", "", "", part("sdc1", "ntfs", winUUID, "My Photos!", "ebd0a0a2-b9e5-4433-87c0-68b6b72699c7", "")),
		disk("sdd", "Kingston", "ext4", "whole-disk-uuid", ""),
	}, ",") + `]}`
}

type partHarness struct {
	*harness
	st *store.Store
}

func newPartHarness(t *testing.T, cacheAt string) partHarness {
	t.Helper()
	h := newHarness(t)
	st, _ := storetest.Open(t)
	h.svc.UsePartitions(st)
	h.svc.statfs = func(string) (uint64, uint64, error) { return 1000, 400, nil }
	h.fake.On("lsblk").Reply(lsblk(false))
	h.fake.On("lsblk", "-J", "-b", "-o", partCols).Reply(partLsblk(cacheAt))
	return partHarness{h, st}
}

func (h partHarness) record(t *testing.T, uuid string) store.PartitionRecord {
	t.Helper()
	recs, err := h.st.Partitions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.UUID == uuid {
			return r
		}
	}
	t.Fatalf("partition %s is not remembered: %+v", uuid, recs)
	return store.PartitionRecord{}
}

// mounts are the mount commands the fake saw, arguments joined.
func mounts(h *harness) []string {
	var out []string
	for _, c := range h.fake.Calls() {
		if c.Name == "mount" {
			out = append(out, strings.Join(c.Args, " "))
		}
	}
	return out
}

func TestPartitionsListedAreTheMountableOnes(t *testing.T) {
	t.Parallel()
	h := newPartHarness(t, "")
	st, err := h.svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range st.Partitions {
		got = append(got, fmt.Sprintf("%s %s %s on %s as %s", p.Path, p.Fstype, p.State, p.Disk, p.MountName))
	}
	// Not the ESP, the root slot, its verity partition, swap, /var (the
	// system mounted it), the bcachefs disk or the LUKS disk.
	want := []string{
		"/dev/nvme0n1p3 ext4 unmounted on Samsung SSD 990 PRO 2TB as cache",
		"/dev/sdc1 ntfs unmounted on Elements as my-photos",
		"/dev/sdd ext4 unmounted on Kingston as disk",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("partitions:\n got %q\nwant %q", got, want)
	}
}

func TestPartitionMountedByTheDaemonIsListedWithItsRoom(t *testing.T) {
	t.Parallel()
	h := newPartHarness(t, "/var/mnt/stuff")
	st, err := h.svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := st.Partitions[0]
	if p.State != "mounted" || p.Mountdir != "/var/mnt/stuff" || p.MountName != "stuff" || p.Usage == nil || p.Usage.Free != 400 {
		t.Fatalf("partition = %+v usage %+v", p, p.Usage)
	}
}

func TestMountPartition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("by its label, remembered for the boot", func(t *testing.T) {
		h := newPartHarness(t, "")
		h.fake.On("mount").Reply("")
		h.fake.On("chattr").Reply("")
		if err := h.svc.MountPartition(ctx, cacheUUID, "", false); err != nil {
			t.Fatal(err)
		}
		if got, want := mounts(h.harness), []string{"-t ext4 -o nodev,nosuid UUID=" + cacheUUID + " /var/mnt/cache"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("mount = %q, want %q", got, want)
		}
		if r := h.record(t, cacheUUID); !r.AutoMount || r.Name != "cache" || r.Fstype != "ext4" || r.ReadOnly {
			t.Fatalf("remembered = %+v", r)
		}
	})
	t.Run("ntfs read-only under a name", func(t *testing.T) {
		h := newPartHarness(t, "")
		h.fake.On("mount").Reply("")
		if err := h.svc.MountPartition(ctx, winUUID, "photos", true); err != nil {
			t.Fatal(err)
		}
		if got, want := mounts(h.harness), []string{"-t ntfs3 -o nodev,nosuid,ro UUID=" + winUUID + " /var/mnt/photos"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("mount = %q, want %q", got, want)
		}
		if r := h.record(t, winUUID); !r.ReadOnly {
			t.Fatalf("remembered = %+v", r)
		}
	})
	t.Run("a pool's name is refused", func(t *testing.T) {
		h := newPartHarness(t, "")
		if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: poolUUID, Mountpoint: "/var/mnt/cache"}); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.MountPartition(ctx, cacheUUID, "", false); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a partition's name is refused to a pool", func(t *testing.T) {
		h := newPartHarness(t, "")
		if err := h.st.UpsertPartition(ctx, store.PartitionRecord{UUID: cacheUUID, Name: "pool", Fstype: "ext4", AutoMount: true}); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Mount(ctx, []string{"/dev/sdb"}, ""); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("one that is not listed, a bad name, a mounted one", func(t *testing.T) {
		h := newPartHarness(t, "/var/mnt/cache")
		if err := h.svc.MountPartition(ctx, "var-uuid", "", false); !errors.Is(err, ErrUnknownPartition) {
			t.Errorf("the system's /var: err = %v", err)
		}
		if err := h.svc.MountPartition(ctx, winUUID, "../x", false); !errors.Is(err, ErrMountName) {
			t.Errorf("a path for a name: err = %v", err)
		}
		if err := h.svc.MountPartition(ctx, cacheUUID, "", false); !errors.Is(err, ErrPartitionState) {
			t.Errorf("mounted already: err = %v", err)
		}
	})
	t.Run("a failed mount guards the directory and remembers nothing", func(t *testing.T) {
		h := newPartHarness(t, "")
		h.fake.On("chattr").Reply("")
		h.fake.On("mount").Fail("wrong fs type")
		if err := h.svc.MountPartition(ctx, cacheUUID, "", false); err == nil {
			t.Fatal("expected error")
		}
		if got, want := chattrs(h.harness), []string{"-i /var/mnt/cache", "+i /var/mnt/cache"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("chattr calls = %v, want %v", got, want)
		}
		if recs, _ := h.st.Partitions(ctx); len(recs) != 0 {
			t.Fatalf("remembered = %+v", recs)
		}
	})
}

func TestUnmountPartitionStopsTheBootMount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newPartHarness(t, "/var/mnt/cache")
	h.fake.On("umount").Reply("")
	h.fake.On("chattr").Reply("")
	if err := h.st.UpsertPartition(ctx, store.PartitionRecord{UUID: cacheUUID, Name: "cache", Fstype: "ext4", AutoMount: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.UnmountPartition(ctx, cacheUUID); err != nil {
		t.Fatal(err)
	}
	if r := h.record(t, cacheUUID); r.AutoMount || r.Name != "cache" {
		t.Fatalf("remembered = %+v", r)
	}
	if got, want := chattrs(h.harness), []string{"+i /var/mnt/cache"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chattr calls = %v, want %v", got, want)
	}
}

func TestAutoMountPartitions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newPartHarness(t, "")
	h.fake.On("mount").Reply("")
	h.fake.On("chattr").Reply("")
	for _, r := range []store.PartitionRecord{
		{UUID: cacheUUID, Name: "cache", Fstype: "ext4", AutoMount: true},
		{UUID: winUUID, Name: "photos", Fstype: "ntfs", ReadOnly: true, AutoMount: false},
		{UUID: "unplugged", Name: "usb", Fstype: "exfat", AutoMount: true},
	} {
		if err := h.st.UpsertPartition(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	h.svc.AutoMount(ctx)
	if got, want := mounts(h.harness), []string{"-t ext4 -o nodev,nosuid UUID=" + cacheUUID + " /var/mnt/cache"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mount = %q, want %q", got, want)
	}
	// The one that is not there: its directory takes no writes.
	if got := chattrs(h.harness); !reflect.DeepEqual(got, []string{"-i /var/mnt/cache", "+i /var/mnt/usb"}) {
		t.Fatalf("chattr calls = %v", got)
	}
}

func TestPartitionName(t *testing.T) {
	t.Parallel()
	for label, want := range map[string]string{
		"cache": "cache", "My Photos!": "my-photos", "": "disk", "---": "disk", "Ünï": "n",
		strings.Repeat("a", 40): strings.Repeat("a", 31), "BACKUP_2024": "backup-2024",
	} {
		if got := partitionName(label); got != want {
			t.Errorf("partitionName(%q) = %q, want %q", label, got, want)
		}
	}
}
