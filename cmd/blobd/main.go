package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/darvell/blob/internal/server"
)

var version = "0.1.0"

func main() {
	cfg := server.DefaultConfig()
	listen := flag.String("listen", envOr("BLOB_LISTEN", cfg.Listen), "listen address")
	token := flag.String("token", os.Getenv("BLOB_TOKEN"), "bearer token (or set BLOB_TOKEN)")
	base := flag.String("base-domain", envOr("BLOB_BASE_DOMAIN", cfg.BaseDomain), "base domain for default hostnames")
	dc := flag.String("dc", envOr("BLOB_DATACENTER", cfg.Datacenter), "Nomad datacenter")
	registry := flag.String("registry", envOr("BLOB_REGISTRY", cfg.Registry), "container registry")
	stateDir := flag.String("state", envOr("BLOB_STATE_DIR", cfg.StateDir), "state dir")
	creds := flag.String("registry-creds", envOr("BLOB_REGISTRY_CREDS", cfg.RegistryCreds), "registry credentials file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.Printf("blobd %s", version)
		return
	}

	cfg.Listen = *listen
	cfg.Token = *token
	cfg.BaseDomain = *base
	cfg.Datacenter = *dc
	cfg.Registry = *registry
	cfg.StateDir = *stateDir
	cfg.JobsDir = *stateDir + "/jobs"
	cfg.SourcesDir = *stateDir + "/sources"
	cfg.RegistryCreds = *creds

	for _, d := range []string{cfg.StateDir, cfg.JobsDir, cfg.SourcesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatalf("mkdir %s: %v", d, err)
		}
	}

	srv := server.New(cfg)
	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: deploy POSTs can be long.
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	go func() {
		log.Printf("blobd %s listening on %s", version, cfg.Listen)
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down")
	shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	_ = hs.Shutdown(shutdownCtx)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
