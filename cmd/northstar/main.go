package main

import (
	"os"

	log "github.com/sirupsen/logrus"

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

	log.Printf(`


	┌─┐┌─┐┬  ┬┌─┐┌─┐┌─┐
	├┤ │  │  │├─┘└─┐│ │
	└─┘└─┘┴─┘┴┴  └─┘└─┘
	High-performance DNS daemon
	v%s


	`, northstar_version)

	err := backend.StartDaemon(zone_dir, host, port, tlsCert, tlsKey, dotPort)

	if err != nil {
		log.Fatalf("Failed to start DNS server: %s\n", err.Error())
	}
}
