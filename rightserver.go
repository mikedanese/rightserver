// Package rightserver implements a dialer and listener using SCM_RIGHTS and
// socket pairs. An unnamed unix socket is created for the root client/server.
// When a new connection is requested by the client, the client creates a
// socketpair and passes half of the connection to the server via the root
// client/server socket.
package rightserver

import (
	"context"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func NewPair() (*Pair, error) {
	fds, err := unix.Socketpair(unix.AF_LOCAL, unix.SOCK_DGRAM, 0)
	if err != nil {
		return nil, wrapSyscallError("socketpair", err)
	}
	server, err := NewSocketConn(fds[0])
	if err != nil {
		return nil, err
	}
	client, err := NewSocketConn(fds[1])
	if err != nil {
		return nil, err
	}
	return &Pair{
		server: server,
		client: client,
	}, nil
}

func NewSocketConn(fd int) (*net.UnixConn, error) {
	f := os.NewFile(uintptr(fd), "")
	if f == nil {
		return nil, fmt.Errorf("invalid file descriptor")
	}
	defer f.Close()

	fconn, err := net.FileConn(f)
	if err != nil {
		return nil, err
	}

	conn, ok := fconn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("couldn't get raw conn from file conn of type %T", conn)
	}

	return conn, nil
}

type Pair struct {
	server *net.UnixConn
	client *net.UnixConn
}

func (p *Pair) Listener() *Listener {
	return NewListener(p.server)
}

func (p *Pair) Dialer() *Dialer {
	return NewDialer(p.client)
}

func NewListener(serverSocket *net.UnixConn) *Listener {
	return &Listener{
		conn: serverSocket,
	}
}

type Listener struct {
	conn *net.UnixConn
}

// Accept waits for and returns the next connection to the listener.
func (l *Listener) Accept() (net.Conn, error) {
	f, err := l.recvFile()
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return net.FileConn(f)
}

// Close closes the listener.
// Any blocked Accept operations will be unblocked and return errors.
func (l *Listener) Close() error {
	return l.conn.Close()
}

// Addr returns the listener's network address.
func (l *Listener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

// File returns an *os.File corresponding to the listener's socket file. The
// underlying file descriptor is dup'd and independent of the listener. It is
// the caller's responsibility to close the file.
func (l *Listener) File() (*os.File, error) {
	return dupConn(l.conn)
}

// fd is always int32, so sizeof(fd) = 4. oob buffer needs room for one fd.
var oobBytes = unix.CmsgSpace(4)

func (l *Listener) recvFile() (*os.File, error) {
	oob := make([]byte, oobBytes)

	_, oobn, _, _, err := l.conn.ReadMsgUnix(nil, oob)
	if err != nil {
		return nil, err
	}
	if oobn != oobBytes {
		return nil, fmt.Errorf("recvfile: incorrect number of bytes read: n=%d", oobn)
	}

	scms, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}
	if len(scms) != 1 {
		return nil, fmt.Errorf("recvfile: number of SCMs is not 1: %d", len(scms))
	}

	fds, err := unix.ParseUnixRights(&scms[0])
	if err != nil {
		return nil, err
	}
	if len(fds) != 1 {
		return nil, fmt.Errorf("recvfile: number of fds is not 1: %d", len(fds))
	}
	return os.NewFile(uintptr(fds[0]), ""), nil
}

func NewDialer(clientSocket *net.UnixConn) *Dialer {
	return &Dialer{
		conn: clientSocket,
	}
}

type Dialer struct {
	conn *net.UnixConn
}

func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	fds, err := unix.Socketpair(unix.AF_LOCAL, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, wrapSyscallError("socketpair", err)
	}

	srvFile := os.NewFile(uintptr(fds[0]), "")
	defer srvFile.Close()

	cliFile := os.NewFile(uintptr(fds[1]), "")
	defer cliFile.Close()

	if err := d.sendFile(srvFile); err != nil {
		return nil, err
	}

	return net.FileConn(cliFile)
}

func (d *Dialer) sendFile(f *os.File) error {
	oob := unix.UnixRights(int(f.Fd()))
	_, _, err := d.conn.WriteMsgUnix(nil, oob, nil)
	return err
}

func (d *Dialer) Close() error {
	return d.conn.Close()
}

// File returns an *os.File corresponding to the dialer's socket file. The
// underlying file descriptor is dup'd and independent of the listener. It is
// the caller's responsibility to close the file.
func (d *Dialer) File() (*os.File, error) {
	return dupConn(d.conn)
}

func wrapSyscallError(name string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(unix.Errno); ok {
		err = os.NewSyscallError(name, err)
	}
	return err
}

func dupConn(conn *net.UnixConn) (*os.File, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}

	var (
		newfd int
		operr error
	)
	if err := raw.Control(func(fd uintptr) {
		newfd, operr = unix.Dup(int(fd))
	}); err != nil {
		return nil, err
	}
	if operr != nil {
		return nil, wrapSyscallError("dup", err)
	}

	return os.NewFile(uintptr(newfd), ""), nil
}
