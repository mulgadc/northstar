package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/mulgadc/northstar/pkg/backend"
)

const northstar_version = "2.0.0"

func main() {
	var zone_dir = os.Getenv("ZONE_DIR")
	if zone_dir == "" {
		zone_dir = "config/domains/"
	}

	var host = os.Getenv("HOST")
	if host == "" {
		host = "0.0.0.0"
	}

	var port = os.Getenv("PORT")
	if port == "" {
		port = "53"
	}

	var tlsCert = os.Getenv("NORTHSTAR_TLS_CERT")
	var tlsKey = os.Getenv("NORTHSTAR_TLS_KEY")
	var dotPort = os.Getenv("DOT_PORT")

	fmt.Printf(`


	┌─┐┌─┐┬  ┬┌─┐┌─┐┌─┐
	├┤ │  │  │├─┘└─┐│ │
	└─┘└─┘┴─┘┴┴  └─┘└─┘
	High-performance DNS daemon
	v%s


	`, northstar_version)

	err := backend.StartDaemon(zone_dir, host, port, tlsCert, tlsKey, dotPort)

	if err != nil {
		slog.Error("failed to start DNS server", "error", err)
		os.Exit(1)
	}
}
