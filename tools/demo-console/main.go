package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var assets embed.FS

type console struct {
	cfg    config
	runner *runner
	token  string
	port   string
	client *http.Client
}

func main() {
	if runtime.GOOS == "windows" {
		log.Fatal("run the demo console inside WSL so trial cancellation can restore services")
	}
	repo := flag.String("repo", ".", "application repository")
	envFile := flag.String("env-file", "", "literal local .env profile")
	evidence := flag.String("evidence-dir", "", "private run directory")
	specDir := flag.String("spec-dir", "", "directory containing the supplied course PDFs")
	listen := flag.String("listen", "127.0.0.1:8099", "loopback address")
	flag.Parse()
	host, port, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("--listen must use a loopback IP and port")
	}
	cfg, err := loadConfig(*repo, *envFile, *evidence, *specDir)
	if err != nil {
		log.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		log.Fatal(err)
	}
	c := &console{cfg: cfg, runner: newRunner(cfg), token: hex.EncodeToString(key), port: port, client: &http.Client{Timeout: 6 * time.Second}}
	server := &http.Server{Addr: *listen, Handler: c.handler(), ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c.runner.cancelActive()
		shutdown, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		c.runner.wait(shutdown)
		_ = server.Shutdown(shutdown)
	}()
	fmt.Printf("Demo console: http://%s\nProject: %s\nRepository: %s\n", *listen, cfg.project, cfg.repo)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func (c *console) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		profile := c.runner.maskedProfile()
		revision, _ := c.runner.capture(r.Context(), "git", "rev-parse", "--short", "HEAD")
		respond(w, 200, map[string]any{"token": c.token, "repo": c.cfg.repo, "project": c.cfg.project, "revision": strings.TrimSpace(string(revision)), "profile": profile, "actions": actionList, "evidence_dir": c.cfg.evidence})
	})
	mux.HandleFunc("GET /api/status", c.status)
	mux.HandleFunc("GET /api/inspect/{kind}", c.inspect)
	mux.HandleFunc("GET /api/logs/{service}", c.logs)
	mux.HandleFunc("GET /api/historical", c.historical)
	mux.HandleFunc("POST /api/runs", c.startRun)
	mux.HandleFunc("GET /api/runs", c.listRuns)
	mux.HandleFunc("GET /api/runs/{id}", c.getRun)
	mux.HandleFunc("POST /api/runs/{id}/cancel", c.cancelRun)
	mux.HandleFunc("GET /api/runs/{id}/{file}", c.runFile)
	mux.HandleFunc("GET /api/spec/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := map[string]string{"m1": "Milestone 1 Specification IF4031 - Tugas Besar - M1.pdf", "central": "IF4031 - Tugas Besar - Dokumen Terpusat.pdf"}[r.PathValue("name")]
		if name == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(c.cfg.specDir, name))
	})
	web, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(web)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "cross-site request rejected", 403)
			return
		}
		if r.Host != "127.0.0.1:"+c.port && r.Host != "localhost:"+c.port && r.Host != "[::1]:"+c.port {
			http.Error(w, "invalid host", 403)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; frame-src 'self'; frame-ancestors 'none'; object-src 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "invalid origin", 403)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Console-Token")), []byte(c.token)) != 1 {
				http.Error(w, "invalid console token", 403)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, err error) {
	respond(w, status, map[string]string{"error": err.Error()})
}
