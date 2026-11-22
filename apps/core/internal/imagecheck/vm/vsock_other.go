//go:build !linux

package vm

import (
	"errors"
	"net"
	"time"
)

var errNoVsock = errors.New("vsock is Linux only")

// VsockAvailable says whether this host can give a machine a vsock device.
func VsockAvailable() error { return errNoVsock }

type notifyListener struct{}

func listenNotify(uint32) (*notifyListener, error)           { return nil, errNoVsock }
func (l *notifyListener) serve(func(cid uint32, msg []byte)) {}
func (l *notifyListener) Close() error                       { return nil }

// DialVsock connects to a port of a guest.
func DialVsock(cid, port uint32, timeout time.Duration) (net.Conn, error) { return nil, errNoVsock }
