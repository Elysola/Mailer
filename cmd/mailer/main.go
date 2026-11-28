// mailer is the binary entrypoint. All logic lives in internal/cli and the
// component packages; this file only wires the process lifecycle.
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

	"github.com/elysola/mailer/internal/cli"
	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/daemon"

	// Component registrations. Adding a component type upstream never
	// touches this file — these imports merely link the built-ins.
	_ "github.com/elysola/mailer/internal/llm/openai"
	_ "github.com/elysola/mailer/internal/notifiers/discord"
	_ "github.com/elysola/mailer/internal/notifiers/gotify"
	_ "github.com/elysola/mailer/internal/notifiers/ntfy"
	_ "github.com/elysola/mailer/internal/notifiers/slack"
	_ "github.com/elysola/mailer/internal/notifiers/telegram"
	_ "github.com/elysola/mailer/internal/notifiers/webhook"
	_ "github.com/elysola/mailer/internal/processors/exec"
	_ "github.com/elysola/mailer/internal/processors/filter"
	_ "github.com/elysola/mailer/internal/processors/header"
	_ "github.com/elysola/mailer/internal/processors/llm"
	_ "github.com/elysola/mailer/internal/processors/regex"
	_ "github.com/elysola/mailer/internal/processors/set"
	_ "github.com/elysola/mailer/internal/processors/templateproc"
	_ "github.com/elysola/mailer/internal/processors/throttle"
	_ "github.com/elysola/mailer/internal/processors/wasm"
	_ "github.com/elysola/mailer/internal/sources/imap"
	_ "github.com/elysola/mailer/internal/sources/maildir"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("mailer: ")

	// Subcommand dispatch. The default (no subcommand) runs the daemon.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "test":
			os.Exit(cli.RunTest(os.Args[2:]))
		case "plugins":
			os.Exit(cli.RunPlugins(os.Args[2:]))
		case "auth":
			os.Exit(cli.RunAuth(os.Args[2:]))
		case "queue":
			os.Exit(cli.RunQueue(os.Args[2:]))
		case "doctor":
			os.Exit(cli.RunDoctor(os.Args[2:]))
		case "migrate-config":
			os.Exit(cli.RunMigrateConfig(os.Args[2:]))
		case "version", "-version", "--version", "-v":
			fmt.Println(VersionString())
			os.Exit(0)
		case "-h", "--help", "help":
			cli.Usage()
			os.Exit(0)
		}
	}

	log.Printf("%s", VersionString())

	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	d, err := daemon.Assemble(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	defer d.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var server *http.Server
	port, httpEnabled := cfg.HealthListenPort()
	if httpEnabled {
		server = newHTTPServer(port, d)
		go func() {
			log.Printf("HTTP server listening on :%d", port)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("http server: %v", err)
			}
		}()
	} else {
		log.Print("health HTTP server disabled (health_port: 0)")
	}

	runErr := d.Run(ctx)

	if server != nil {
		if err := server.Shutdown(context.Background()); err != nil {
			log.Printf("http shutdown: %v", err)
		}
	}
	if runErr != nil {
		log.Fatalf("daemon: %v", runErr)
	}
	log.Print("bye")
}

// newHTTPServer builds the health/status/metrics mux for the given port.
func newHTTPServer(port int, d *daemon.Daemon) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, d.Metrics().Snapshot())
	})
	// /status exposes non-sensitive per-source health (no hostnames,
	// usernames or error text). /health stays a pure liveness probe.
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		out := struct {
			Status  string                         `json:"status"`
			Sources map[string]daemon.SourceStatus `json:"sources"`
		}{Status: "ok", Sources: d.Status()}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			log.Printf("status encode: %v", err)
		}
	})

	return &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}
}
