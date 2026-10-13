package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/mu-zi-lee/magpie-intelligence-check/internal/check"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	dir := flag.String("data", "data", "private data directory")
	gate := flag.String("gate", "plugin/gate.middleware.js", "middleware entrypoint")
	listen := flag.String("listen", "127.0.0.1:0", "loopback address")
	cloudflared := flag.String("cloudflared", "", "official cloudflared executable")
	owned := flag.Bool("owned", false, "stop when parent input closes")
	flag.Parse()
	if err := run(*dir, *gate, *listen, *cloudflared, *owned); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(dir, gate, addr, binary string, owned bool) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(dir, "service.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("此数据目录已有检测服务运行，请关闭另一个 Magpie 实例或使用独立 dataDir")
	}
	defer lock.Unlock()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("management listener must be loopback")
	}
	abs, err := filepath.Abs(gate)
	if err != nil {
		return err
	}
	s, err := check.New(dir, abs)
	if err != nil {
		return err
	}
	defer s.Close()
	s.WatchLog()
	s.WatchTargets()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	token := check.Secret()
	origin := "http://" + ln.Addr().String()
	access := check.NewAccess(token, origin)
	console := check.NewConsole(access, origin, dir, binary)
	defer console.Close()
	server := &http.Server{Handler: s.ManagedHandler(access, console), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	json.NewEncoder(os.Stdout).Encode(map[string]string{"url": origin + "/open/" + access.Ticket(), "origin": origin, "control": token})
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	done := make(chan error, 1)
	parentClosed := make(chan struct{})
	if owned {
		go func() { io.Copy(io.Discard, os.Stdin); close(parentClosed) }()
	}
	go func() { done <- server.Serve(ln) }()
	select {
	case err := <-done:
		if err != http.ErrServerClosed {
			return err
		}
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	case <-parentClosed:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}
	return nil
}
