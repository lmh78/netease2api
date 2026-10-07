package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"netease2api/internal/gateway"
	"netease2api/internal/netease"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	dir := flag.String("data", "data", "private state directory")
	importPath := flag.String("import", "", "import a Cookie/JSON/JSONL file at startup")
	initOnly := flag.Bool("init-only", false, "initialize keys/import without starting the server")
	timeout := flag.Duration("timeout", 90*time.Second, "maximum chat duration including queue wait")
	poll := flag.Duration("poll-interval", 2*time.Second, "history polling interval")
	apiURL := flag.String("upstream-api", "", "override the NetEase API base URL")
	loginURL := flag.String("upstream-login", "", "override the NetEase PE login base URL")
	flag.Parse()
	if *timeout < time.Second || *poll < time.Second {
		return fmt.Errorf("timeout/poll-interval must be at least 1s")
	}
	if _, _, err := net.SplitHostPort(*addr); err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	absolute, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	store, err := gateway.NewStore(absolute)
	if err != nil {
		return err
	}
	access, err := gateway.LoadAccess(absolute)
	if err != nil {
		return err
	}
	if err = store.EnsureDefaultKey(access.APIKey); err != nil {
		return err
	}
	if *importPath != "" {
		data, e := os.ReadFile(*importPath)
		if e != nil {
			return e
		}
		added, updated, e := store.Import(string(data))
		if e != nil {
			return e
		}
		log.Printf("Imported accounts: added=%d updated=%d", added, updated)
	}
	fmt.Printf("netease2api 0.3.0\nAccess keys: %s\n", filepath.Join(absolute, "access.local.json"))
	if *initOnly {
		return nil
	}
	client := netease.NewClient(25 * time.Second)
	for _, base := range []string{*apiURL, *loginURL} {
		if base != "" && !validURL(base) {
			return fmt.Errorf("upstream URLs must be http(s) base URLs")
		}
	}
	if *apiURL != "" {
		client.APIBaseURL = strings.TrimRight(*apiURL, "/")
	}
	if *loginURL != "" {
		client.PECoreBaseURL = strings.TrimRight(*loginURL, "/")
	}
	engine := &gateway.Engine{Store: store, Upstream: &gateway.FoxUpstream{Client: client, PollInterval: *poll, Skin: "狐狸"}, Timeout: *timeout}
	app := gateway.NewServer(engine, access.AdminKey)
	server := &http.Server{Addr: *addr, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	defer close(done)
	fmt.Printf("Management: http://%s/\nOpenAI base URL: http://%s/v1\nModel: netease-fox\n", *addr, *addr)
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
