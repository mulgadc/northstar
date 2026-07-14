package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mulgadc/northstar/pkg/config"
	"github.com/mulgadc/northstar/pkg/server"
	"github.com/mulgadc/northstar/pkg/telemetry"
)

// Version is set via ldflags at build time.
// Example: go build -ldflags "-X main.Version=v1.0.0"
var Version = "dev"

func main() {
	configPath := flag.String("config", "", "path to northstar.toml")
	flag.Parse()

	level := logLevel()

	// Telemetry is best-effort: a failed init never blocks the DNS daemon.
	otelShutdown, err := telemetry.Init(context.Background(), "northstar")
	if err != nil {
		slog.Warn("Telemetry init failed, continuing without export", "error", err)
	} else {
		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := otelShutdown(flushCtx); err != nil {
				slog.Warn("Telemetry shutdown", "error", err)
			}
		}()
	}

	telemetry.SetDefaultJSONLogger(level)

	fmt.Printf(`


	┌─┐┌─┐┬  ┬┌─┐┌─┐┌─┐
	├┤ │  │  │├─┘└─┐│ │
	└─┘└─┘┴─┘┴┴  └─┘└─┘
	High-performance DNS daemon
	v%s


	`, Version)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	srv, err := server.NewServer(cfg)
	if err != nil {
		slog.Error("failed to create server", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		slog.Error("failed to start DNS server", "error", err)
		os.Exit(1)
	}

	<-ctx.Done()
	slog.Info("shutting down")
	if err := srv.Shutdown(context.Background()); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}

// loadConfig reads northstar.toml when --config is given, otherwise builds a
// config from environment variables for backward-compatible standalone use.
func loadConfig(path string) (config.ServerConfig, error) {
	if path != "" {
		return config.LoadServerConfig(path)
	}

	zoneDir := envOr("ZONE_DIR", "config/domains/")
	host := envOr("HOST", "0.0.0.0")
	port := envOr("PORT", "53")

	cfg := config.ServerConfig{
		Listen:        fmt.Sprintf("%s:%s", host, port),
		DotListen:     dotListen(host),
		TLSCert:       os.Getenv("NORTHSTAR_TLS_CERT"),
		TLSKey:        os.Getenv("NORTHSTAR_TLS_KEY"),
		DefaultDomain: os.Getenv("NORTHSTAR_DEFAULT_DOMAIN"),
		ZoneDir:       zoneDir,
		Upstream: config.UpstreamConfig{
			Nameservers: splitCSV(os.Getenv("NORTHSTAR_UPSTREAM")),
		},
	}

	if endpoint := os.Getenv("NORTHSTAR_S3_ENDPOINT"); endpoint != "" {
		bucket := strings.TrimPrefix(zoneDir, "s3://")
		cfg.ZoneDir = ""
		cfg.S3 = config.S3Config{
			Endpoint:  endpoint,
			Region:    os.Getenv("AWS_REGION"),
			Bucket:    bucket,
			AccessKey: os.Getenv("AWS_ACCESS_KEY"),
			SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			Insecure:  os.Getenv("NORTHSTAR_S3_INSECURE") != "",
		}
	}

	return cfg, nil
}

// logLevel builds the slog level from NORTHSTAR_LOG_IGNORE / NORTHSTAR_LOG_DEBUG,
// for telemetry.SetDefaultJSONLogger.
func logLevel() *slog.LevelVar {
	level := new(slog.LevelVar)
	if _, ok := os.LookupEnv("NORTHSTAR_LOG_IGNORE"); ok {
		level.Set(slog.LevelError + 4)
	}
	if _, ok := os.LookupEnv("NORTHSTAR_LOG_DEBUG"); ok {
		level.Set(slog.LevelDebug)
	}
	return level
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func dotListen(host string) string {
	if os.Getenv("NORTHSTAR_TLS_CERT") == "" {
		return ""
	}
	port := envOr("DOT_PORT", "853")
	return fmt.Sprintf("%s:%s", host, port)
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
