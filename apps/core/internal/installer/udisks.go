package installer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
)

// Disks lists the machine's disks and deletes partitions: UDisks2 on the
// stick, a fake in tests.
type Disks interface {
	List(ctx context.Context) ([]InstallDisk, error)
	DeletePartition(ctx context.Context, path string) error
}

// UDisks reads and changes disks through UDisks2 on the system bus, the
// backend GNOME Disks and Cockpit use.
type UDisks struct{}

const (
	udBus      = "org.freedesktop.UDisks2"
	udRoot     = "/org/freedesktop/UDisks2"
	ifBlock    = "org.freedesktop.UDisks2.Block"
	ifPart     = "org.freedesktop.UDisks2.Partition"
	ifTable    = "org.freedesktop.UDisks2.PartitionTable"
	ifFS       = "org.freedesktop.UDisks2.Filesystem"
	ifDrive    = "org.freedesktop.UDisks2.Drive"
	ifObjects  = "org.freedesktop.DBus.ObjectManager.GetManagedObjects"
	noDriveObj = dbus.ObjectPath("/")
)

type objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

func (UDisks) objects(ctx context.Context) (*dbus.Conn, objects, error) {
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, nil, fmt.Errorf("system bus: %w", err)
	}
	var objs objects
	if err := conn.Object(udBus, udRoot).CallWithContext(ctx, ifObjects, 0).Store(&objs); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("UDisks2: %w", err)
	}
	return conn, objs, nil
}

// List returns every whole disk that has a drive (no loop, zram or device
// mapper devices), with its partitions.
func (u UDisks) List(ctx context.Context) ([]InstallDisk, error) {
	conn, objs, err := u.objects(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return fromObjects(objs), nil
}

func fromObjects(objs objects) []InstallDisk {
	disks := map[dbus.ObjectPath]*InstallDisk{}
	var order []dbus.ObjectPath
	for path, ifs := range objs {
		b, ok := ifs[ifBlock]
		if !ok || ifs[ifPart] != nil {
			continue
		}
		drive := objPath(b["Drive"])
		if drive == noDriveObj || drive == "" || boolOf(b["HintIgnore"]) {
			continue
		}
		// Optical drives without a disc, card readers without a card.
		if u64(b["Size"]) < gib {
			continue
		}
		dev := bytesStr(b["Device"])
		d := &InstallDisk{Name: filepath.Base(dev), Path: dev, Size: u64(b["Size"]), Table: "none"}
		if t, ok := ifs[ifTable]; ok {
			d.Table = str(t["Type"])
		}
		if dr, ok := objs[drive][ifDrive]; ok {
			d.Model = strings.TrimSpace(str(dr["Vendor"]) + " " + str(dr["Model"]))
			d.Removable = boolOf(dr["Removable"]) || boolOf(dr["MediaRemovable"])
			d.Bus = str(dr["ConnectionBus"])
		}
		disks[path] = d
		order = append(order, path)
	}
	for _, ifs := range objs {
		p, ok := ifs[ifPart]
		if !ok {
			continue
		}
		d := disks[objPath(p["Table"])]
		if d == nil {
			continue
		}
		b := ifs[ifBlock]
		dev := bytesStr(b["Device"])
		fs := str(b["IdType"])
		label := str(p["Name"])
		if label == "" {
			label = str(b["IdLabel"])
		}
		typ := strings.ToUpper(str(p["Type"]))
		d.Partitions = append(d.Partitions, InstallPartition{
			Number: int(u32(p["Number"])), Name: filepath.Base(dev), Path: dev,
			Start: u64(p["Offset"]), Size: u64(p["Size"]), Type: typ,
			Holds: holds(typ, fs, str(p["Name"])), Label: label, FS: fs,
		})
		if fsIf, ok := ifs[ifFS]; ok && len(mountPoints(fsIf["MountPoints"])) > 0 {
			d.InUse = true
		}
	}
	out := make([]InstallDisk, 0, len(order))
	for _, path := range order {
		d := disks[path]
		sortParts(d.Partitions)
		out = append(out, assess(*d))
	}
	sortDisks(out)
	return out
}

// DeletePartition deletes the partition whose device is path.
func (u UDisks) DeletePartition(ctx context.Context, path string) error {
	conn, objs, err := u.objects(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for obj, ifs := range objs {
		if _, ok := ifs[ifPart]; ok && bytesStr(ifs[ifBlock]["Device"]) == path {
			return conn.Object(udBus, obj).CallWithContext(ctx, ifPart+".Delete", 0, map[string]dbus.Variant{}).Err
		}
	}
	return fmt.Errorf("no partition %s", path)
}

func mountPoints(v dbus.Variant) [][]byte {
	m, _ := v.Value().([][]byte)
	return m
}

func bytesStr(v dbus.Variant) string {
	b, _ := v.Value().([]byte)
	return strings.TrimRight(string(b), "\x00")
}

func str(v dbus.Variant) string  { s, _ := v.Value().(string); return s }
func u64(v dbus.Variant) uint64  { n, _ := v.Value().(uint64); return n }
func u32(v dbus.Variant) uint32  { n, _ := v.Value().(uint32); return n }
func boolOf(v dbus.Variant) bool { b, _ := v.Value().(bool); return b }
func objPath(v dbus.Variant) dbus.ObjectPath {
	p, _ := v.Value().(dbus.ObjectPath)
	return p
}
