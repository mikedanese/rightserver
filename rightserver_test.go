package rightserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newHTTPServer(t *testing.T, l net.Listener, h http.Handler) *httptest.Server {
	server := httptest.NewUnstartedServer(h)
	server.Listener.Close()
	server.Listener = l
	t.Cleanup(server.Close)
	server.Start()
	return server
}

func newPair(t *testing.T) *Pair {
	sp, err := NewPair()
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

func TestHTTPServer(t *testing.T) {
	t.Cleanup(func() {
		if fds := openSockets(t); len(fds) != 0 {
			t.Errorf("file descriptors leaked: n=%d, fds=%v", len(fds), fds)
		}
	})

	sp := newPair(t)
	defer sp.Dialer().Close()

	newHTTPServer(t, sp.Listener(), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintln(w, "Hello")
	}))

	cli := &http.Client{
		Transport: &http.Transport{
			DialContext:     sp.Dialer().DialContext,
			IdleConnTimeout: 1 * time.Second,
		},
	}

	for i := 0; i < 3; i++ {
		req, err := http.NewRequest(http.MethodGet, "http://localhost/hi", nil)
		if err != nil {
			t.Fatal(err)
		}

		resp, err := cli.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("error closing request body: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("unexpected status: %v, expected: %v", resp.StatusCode, http.StatusOK)
		}
	}

	cli.CloseIdleConnections()
}

type pingPongServer struct {
	t        *testing.T
	listener *Listener
	dialer   *Dialer
	stopCh   chan struct{}
}

func (s *pingPongServer) run() {
	defer close(s.stopCh)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.t.Errorf("failed to accept connection: %v", err)
			}
			return
		}
		go s.handle(conn)
	}
}

func (s *pingPongServer) handle(conn net.Conn) {
	defer conn.Close()

	var buf [4]byte
	n, err := conn.Read(buf[:])
	if err != nil {
		s.t.Errorf("failed to read from server side conn: %v", err)
		return
	}
	if n != 4 {
		s.t.Errorf("unexpected number of bytes read: %d", n)
		return
	}
	if !bytes.Equal(buf[:], []byte("ping")) {
		s.t.Errorf("unexpected message: %s", string(buf[:]))
		return
	}

	if _, err := io.WriteString(conn, "pong"); err != nil {
		s.t.Errorf("failed to write to server side conn: %v", err)
		return
	}
}

func (s *pingPongServer) pingPong(ctx context.Context, t *testing.T) {
	conn, err := s.dialer.DialContext(ctx, "unix", "@")
	if err != nil {
		s.t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	if _, err := io.WriteString(conn, "ping"); err != nil {
		s.t.Fatalf("failed to write to client side conn: %v", err)
	}

	var buf [4]byte
	n, err := conn.Read(buf[:])
	if err != nil {
		s.t.Fatalf("failed to read from client side conn: %v", err)
	}
	if n != 4 {
		s.t.Fatalf("unexpected number of bytes read: %d", n)
	}
	if !bytes.Equal(buf[:], []byte("pong")) {
		s.t.Fatalf("unexpected message: %s", string(buf[:]))
	}
}

func TestPingPong(t *testing.T) {
	t.Cleanup(func() {
		if fds := openSockets(t); len(fds) != 0 {
			t.Errorf("file descriptors leaked: n=%d, fds=%v", len(fds), fds)
		}
	})
	sp := newPair(t)
	t.Cleanup(func() {
		sp.Listener().Close()
		sp.Dialer().Close()
	})

	pps := &pingPongServer{
		t:        t,
		listener: sp.Listener(),
		dialer:   sp.Dialer(),
		stopCh:   make(chan struct{}),
	}

	go pps.run()

	ctx := context.Background()

	var wg sync.WaitGroup
	for n := 0; n < 100; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				pps.pingPong(ctx, t)
			}
		}()
	}
	wg.Wait()
}

func openSockets(t *testing.T) []int {
	dirents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	var openFds []int
	for _, dirent := range dirents {
		info, err := dirent.Info()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		path, err := os.Readlink(filepath.Join("/proc/self/fd", info.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(path, "socket:") {
			continue
		}

		fd, err := strconv.Atoi(info.Name())
		if err != nil {
			t.Fatal(err)
		}

		openFds = append(openFds, fd)
	}

	return openFds
}
