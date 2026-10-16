package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/elysola/mailer/internal/app"
	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/state"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("mailer: ")

	// Subcommand dispatch (e.g. `mailer test`). The default (no subcommand)
	// runs the daemon, preserving backwards compatibility.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "test":
			os.Exit(runTest(os.Args[2:]))
		case "-h", "--help", "help":
			usage()
			os.Exit(0)
		}
		// Anything else falls through to the daemon (and its own flag parse).
	}

	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := state.Load(cfg.StateFile)
	if err != nil {
		log.Fatalf("state: %v", err)
	}
	defer store.Close()

	poller, err := app.New(cfg, store)
	if err != nil {
		store.Close()
		log.Fatalf("init: %v", err)
	}
	defer poller.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, poller.Metrics().Snapshot())
	})
	// /status exposes non-sensitive per-account health (no hostnames, usernames
	// or error text). /health stays a pure liveness probe.
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		status := poller.Status()
		out := struct {
			Status   string                       `json:"status"`
			Accounts map[string]app.AccountStatus `json:"accounts"`
		}{Status: "ok", Accounts: status}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			log.Printf("status encode: %v", err)
		}
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.HealthPort),
		Handler: mux,
	}

	go func() {
		log.Printf("HTTP server listening on :%d", cfg.HealthPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http server: %v", err)
		}
	}()

	poller.Run(ctx)

	if err := server.Shutdown(context.Background()); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	log.Print("bye")
}
