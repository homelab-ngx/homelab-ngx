package vm

import (
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	c := Config{
		OVMFCode: "/fw/code.fd", Vars: "/vm/vars.fd", CPUs: 4, MemMiB: 4096, UUID: "u",
		Order:       true,
		Disks:       []Disk{{USB, "/vm/stick.qcow2"}, {Virtio, "/vm/disk.raw"}, {MegaSAS, "/vm/sys.qcow2"}},
		Credentials: map[string][]byte{"b": []byte("2"), "a": []byte("1")},
		CID:         7, SSHPort: 2222, HTTPPort: 8080, Serial: "/vm/s.log", QMP: "/tmp/q",
	}
	got := strings.Join(c.Args(), " ")
	for _, want := range []string{
		"-smp 4 -m 4096 -uuid u",
		"-drive if=pflash,format=raw,readonly=on,file=/fw/code.fd -drive if=pflash,format=raw,file=/vm/vars.fd",
		"-device qemu-xhci -drive if=none,id=disk0,format=qcow2,file=/vm/stick.qcow2,cache=unsafe,discard=unmap,detect-zeroes=unmap -device usb-storage,drive=disk0,bootindex=0",
		"-drive if=none,id=disk1,format=raw,file=/vm/disk.raw,cache=unsafe,discard=unmap,detect-zeroes=unmap -device virtio-blk-pci,drive=disk1,bootindex=1",
		"-device megasas-gen2,id=scsi2 -drive if=none,id=disk2,format=qcow2,file=/vm/sys.qcow2,cache=unsafe,discard=unmap,detect-zeroes=unmap -device scsi-hd,bus=scsi2.0,dpofua=off,drive=disk2,bootindex=2",
		"-smbios type=11,value=io.systemd.credential.binary:a=MQ== -smbios type=11,value=io.systemd.credential.binary:b=Mg==",
		"-device vhost-vsock-pci,guest-cid=7",
		"-nic user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:2222-:22,hostfwd=tcp:127.0.0.1:8080-:80",
		"-serial file:/vm/s.log -qmp unix:/tmp/q,server,nowait",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "qemu-xhci") != 1 {
		t.Errorf("one xHCI controller for the USB disks: %s", got)
	}
	if c.First() != "stick" {
		t.Errorf("First() = %q", c.First())
	}
}

func TestArgsPlain(t *testing.T) {
	c := Config{OVMFCode: "c", Vars: "v", CPUs: 1, MemMiB: 512, Disks: []Disk{{Virtio, "/d.raw"}}, Serial: "s"}
	got := strings.Join(c.Args(), " ")
	if !strings.Contains(got, "-drive if=virtio,format=raw,file=/d.raw,") || strings.Contains(got, "bootindex") || strings.Contains(got, "-uuid") || strings.Contains(got, "vsock") {
		t.Errorf("args: %s", got)
	}
}
