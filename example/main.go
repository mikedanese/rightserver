package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"github.com/mikedanese/rightserver"
)

const sockFileEnvKey = "SOCK_FILE"

func main() {
	if os.Getenv(sockFileEnvKey) == "" {
		parent()
	} else {
		child()
	}
}

func parent() {
	rsp, err := rightserver.NewPair()
	if err != nil {
		log.Fatal(err)
	}

	startChild(rsp)

	dialer := rsp.Dialer()
	defer dialer.Close()

	cli := &http.Client{
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
	}

	resp, err := cli.Get("http://localhost:8080/")
	if err != nil {
		log.Fatal(err)
	}

	b, _ := httputil.DumpResponse(resp, true)
	log.Print(string(b))

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				resp, err := cli.Get("http://localhost:8080/")
				if err != nil {
					log.Fatal(err)
				}
				resp.Body.Close()
			}
		}()
	}

	wg.Wait()

	des, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		log.Fatal(err)
	}
	for _, de := range des {
		log.Printf("%#v", de)
	}
}

func startChild(rsp *rightserver.Pair) {
	cmd := exec.Command("/proc/self/exe")
	cmd.Env = cmd.Environ()
	cmd.Env = append(cmd.Env, "SOCK_FILE=3")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	sockFile, err := rsp.Listener().File()
	if err != nil {
		log.Fatal(err)
	}

	addHalfToCmd(sockFile, cmd, sockFileEnvKey)
	defer rsp.Listener().Close()

	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	go func() {
		cmd.Wait()
		log.Fatal("child exited")
	}()
}

func child() {
	ln, err := listenerFromEnv(sockFileEnvKey)
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "Hello")
		}),
	}

	if err := srv.Serve(ln); err != nil {
		log.Fatal(err)
	}
}

func addHalfToCmd(sock *os.File, cmd *exec.Cmd, key string) {
	childFD := len(cmd.ExtraFiles) + 3
	cmd.ExtraFiles = append(cmd.ExtraFiles, sock)
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", key, childFD))
}

// listenerFromEnv constructs a listener of the environment variable named by
// the key.
func listenerFromEnv(key string) (*rightserver.Listener, error) {
	val := os.Getenv(key)
	if val == "" {
		return nil, fmt.Errorf("%q environment variable not found", key)
	}
	fd, err := strconv.Atoi(val)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := rightserver.NewSocketConn(fd)
	if err != nil {
		log.Fatal(err)
	}
	return rightserver.NewListener(conn), nil
}

// dialerFromEnv constructs a listener of the environment variable named by the
// key.
func dialerFromEnv(key string) (*rightserver.Dialer, error) {
	val := os.Getenv(key)
	if val == "" {
		return nil, fmt.Errorf("%q environment variable not found", key)
	}
	fd, err := strconv.Atoi(val)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := rightserver.NewSocketConn(fd)
	if err != nil {
		log.Fatal(err)
	}
	return rightserver.NewDialer(conn), nil
}
