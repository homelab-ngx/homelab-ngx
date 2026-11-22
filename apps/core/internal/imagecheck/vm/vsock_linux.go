//go:build linux

package vm

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// VsockAvailable says whether this host can give a machine a vsock device:
// /dev/vhost-vsock opens for reading and writing.
func VsockAvailable() error {
	f, err := os.OpenFile("/dev/vhost-vsock", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

// notifyListener takes the guest's sd_notify messages: systemd's
// vmm.notify_socket=vsock:2:PORT connects with SOCK_SEQPACKET, one
// connection a message.
type notifyListener struct {
	fd   int
	port uint32
}

func listenNotify(port uint32) (*notifyListener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock bind %d: %w", port, err)
	}
	if err := unix.Listen(fd, 64); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return &notifyListener{fd: fd, port: port}, nil
}

// serve reads every message until the listener is closed.
func (l *notifyListener) serve(onMessage func(cid uint32, msg []byte)) {
	for {
		nfd, sa, err := unix.Accept4(l.fd, unix.SOCK_CLOEXEC)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ECONNABORTED) {
				continue
			}
			return
		}
		var cid uint32
		if vm, ok := sa.(*unix.SockaddrVM); ok {
			cid = vm.CID
		}
		go func() {
			defer unix.Close(nfd)
			// A message that never ends would hold the goroutine only.
			tv := unix.NsecToTimeval(int64(30 * time.Second))
			_ = unix.SetsockoptTimeval(nfd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
			buf := make([]byte, 64*1024)
			for {
				n, _, err := unix.Recvfrom(nfd, buf, 0)
				if err != nil || n == 0 {
					return
				}
				onMessage(cid, append([]byte(nil), buf[:n]...))
			}
		}()
	}
}

func (l *notifyListener) Close() error {
	// shutdown wakes a blocked accept; close alone may not.
	_ = unix.Shutdown(l.fd, unix.SHUT_RDWR)
	return unix.Close(l.fd)
}

// DialVsock connects to a port of a guest, as a net.Conn with deadlines.
func DialVsock(cid, port uint32, timeout time.Duration) (net.Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	tv := unix.NsecToTimeval(int64(timeout))
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &tv)
	// The connect timeout of AF_VSOCK.
	_ = unix.SetsockoptTimeval(fd, unix.AF_VSOCK, unix.SO_VM_SOCKETS_CONNECT_TIMEOUT, &tv)
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock connect %d:%d: %w", cid, port, err)
	}
	zero := unix.Timeval{}
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &zero)
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), fmt.Sprintf("vsock:%d:%d", cid, port))
	return &vsockConn{File: f, local: vsockAddr{2, 0}, remote: vsockAddr{cid, port}}, nil
}

type vsockAddr struct{ cid, port uint32 }

func (a vsockAddr) Network() string { return "vsock" }
func (a vsockAddr) String() string  { return fmt.Sprintf("%d:%d", a.cid, a.port) }

// vsockConn is a stream socket as a net.Conn: os.File polls a non-blocking
// descriptor, so reads and writes take deadlines.
type vsockConn struct {
	*os.File
	local, remote vsockAddr
}

func (c *vsockConn) LocalAddr() net.Addr  { return c.local }
func (c *vsockConn) RemoteAddr() net.Addr { return c.remote }
