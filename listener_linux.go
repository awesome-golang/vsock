//+build linux

package vsock

import (
	"net"
	"time"

	"golang.org/x/sys/unix"
)

var _ net.Listener = &listener{}

// A listener is the net.Listener implementation for connection-oriented
// VM sockets.
type listener struct {
	fd   listenFD
	addr *Addr
}

// Addr and Close implement the net.Listener interface for listener.
func (l *listener) Addr() net.Addr                { return l.addr }
func (l *listener) Close() error                  { return l.fd.Close() }
func (l *listener) SetDeadline(t time.Time) error { return l.fd.SetDeadline(t) }

// Accept accepts a single connection from the listener, and sets up
// a net.Conn backed by conn.
func (l *listener) Accept() (net.Conn, error) {
	cfd, sa, err := l.fd.Accept4(0)
	if err != nil {
		return nil, err
	}

	savm := sa.(*unix.SockaddrVM)
	remote := &Addr{
		ContextID: savm.CID,
		Port:      savm.Port,
	}

	return newConn(cfd, l.addr, remote)
}

// listenStream is the entry point for ListenStream on Linux.
func listenStream(port uint32) (*listener, error) {
	var cid uint32
	if err := localContextID(sysFS{}, &cid); err != nil {
		return nil, err
	}

	lfd, err := newListenFD()
	if err != nil {
		return nil, err
	}

	return listenStreamLinuxHandleError(lfd, cid, port)
}

// listenStreamLinuxHandleError ensures that any errors from listenStreamLinux
// result in the socket being cleaned up properly.
func listenStreamLinuxHandleError(lfd listenFD, cid, port uint32) (*listener, error) {
	l, err := listenStreamLinux(lfd, cid, port)
	if err != nil {
		// If any system calls fail during setup, the socket must be closed
		// to avoid file descriptor leaks.
		_ = lfd.EarlyClose()
		return nil, err
	}

	return l, nil
}

// TODO(mdlayher): fine-tune this number instead of just picking one.
const listenBacklog = 32

// listenStreamLinux is the entry point for tests on Linux.
func listenStreamLinux(lfd listenFD, cid, port uint32) (*listener, error) {
	// Zero-value for "any port" is friendlier in Go than a constant.
	if port == 0 {
		port = unix.VMADDR_PORT_ANY
	}

	sa := &unix.SockaddrVM{
		CID:  cid,
		Port: port,
	}

	if err := lfd.Bind(sa); err != nil {
		return nil, err
	}

	if err := lfd.Listen(listenBacklog); err != nil {
		return nil, err
	}

	lsa, err := lfd.Getsockname()
	if err != nil {
		return nil, err
	}

	// Done with blocking mode setup, transition to non-blocking before the
	// caller has a chance to start calling things concurrently that might make
	// the locking situation tricky.
	//
	// Note: if any calls fail after this point, lfd.Close should be invoked
	// for cleanup because the socket is now non-blocking.
	if err := lfd.SetNonblocking("vsock-listen"); err != nil {
		return nil, err
	}

	lsavm := lsa.(*unix.SockaddrVM)
	addr := &Addr{
		ContextID: lsavm.CID,
		Port:      lsavm.Port,
	}

	return &listener{
		fd:   lfd,
		addr: addr,
	}, nil
}
