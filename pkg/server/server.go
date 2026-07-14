// Package server provides an embeddable Northstar DNS server with an explicit
// configuration struct and a Start/Reload/Shutdown lifecycle, replacing the
// former blocking, env-driven backend.StartDaemon entrypoint.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/mulgadc/northstar/pkg/backend"
	"github.com/mulgadc/northstar/pkg/config"
)

// Server is an authoritative DNS server bound to one or more addresses, backed
// by a zone database synced from a filesystem dir or an S3 bucket.
type Server struct {
	cfg     config.ServerConfig
	zoneDB  *config.Config
	handler *backend.Handler

	mu          sync.Mutex
	servers     []*dns.Server
	httpServers []*http.Server
	cancel      context.CancelFunc
}

// NewServer constructs a Server from explicit configuration. It does not bind
// any listeners or read any zones until Start is called.
func NewServer(cfg config.ServerConfig) (*Server, error) {
	upstream := backend.NewUpstream(backend.ParseUpstreamServers(cfg.Upstream.Nameservers))
	zoneDB := &config.Config{
		Records: make(map[config.DomainLookup][]config.Records),
		Domain:  make(map[string]config.Domain),
	}

	return &Server{
		cfg:     cfg,
		zoneDB:  zoneDB,
		handler: backend.NewHandler(zoneDB, upstream),
	}, nil
}

// Start loads zones, binds UDP/TCP (and optional DoT) listeners, and launches
// the live zone-sync loop. It is non-blocking: listeners run in background
// goroutines and bind errors are returned synchronously.
func (s *Server) Start(ctx context.Context) error {
	if err := s.Reload(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	addrs := s.cfg.ListenAddrs()
	if len(addrs) == 0 {
		cancel()
		return fmt.Errorf("no listen addresses configured")
	}

	for _, addr := range addrs {
		if err := s.listenUDPTCP(addr); err != nil {
			cancel()
			_ = s.Shutdown(context.Background())
			return err
		}
	}

	if err := s.listenDoT(); err != nil {
		cancel()
		_ = s.Shutdown(context.Background())
		return err
	}

	if err := s.listenDoH(); err != nil {
		cancel()
		_ = s.Shutdown(context.Background())
		return err
	}

	go s.zoneDB.MonitorConfig(ctx, s.cfg.ZoneSource(), s.cfg.S3Pointer(), s.cfg.SyncDuration())

	return nil
}

// Reload re-reads all zones from the configured source and atomically swaps the
// in-memory zone database. On a source enumeration failure the existing zone DB
// is kept untouched, so a transient S3 outage cannot wipe authoritative data.
func (s *Server) Reload() error {
	fresh, err := config.ReadZoneFiles(s.cfg.ZoneSource(), s.cfg.S3Pointer())
	if err != nil {
		return fmt.Errorf("reload zones: %w", err)
	}

	s.zoneDB.Mu.Lock()
	s.zoneDB.Records = fresh.Records
	s.zoneDB.Domain = fresh.Domain
	s.zoneDB.Mu.Unlock()

	return nil
}

// ReloadZone re-reads a single zone from the configured source and atomically
// swaps just that zone in the live database, leaving all other zones untouched.
// This is the control-plane fast-path: a record change is served immediately
// instead of after the SyncInterval poll, and the cost is one zone fetch rather
// than a full database rebuild.
func (s *Server) ReloadZone(zone string) error {
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	if zone == "" {
		return errors.New("reload zone: empty zone name")
	}
	path := s.cfg.ZoneSource() + "/" + zone + ".toml"
	fresh, err := config.ReadZone(path, time.Now(), s.cfg.S3Pointer())
	if err != nil {
		return fmt.Errorf("reload zone %s: %w", zone, err)
	}
	s.zoneDB.ReplaceZone(fresh)
	return nil
}

// Shutdown stops the sync loop and all listeners.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}

	s.mu.Lock()
	servers := s.servers
	httpServers := s.httpServers
	s.servers = nil
	s.httpServers = nil
	s.mu.Unlock()

	var firstErr error
	for _, srv := range servers {
		if err := srv.ShutdownContext(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, srv := range httpServers {
		if err := srv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Server) listenUDPTCP(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("bind udp %s: %w", addr, err)
	}
	udp := &dns.Server{PacketConn: pc, Handler: &taggedHandler{inner: s.handler, transport: "udp"}}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("bind tcp %s: %w", addr, err)
	}
	tcp := &dns.Server{Listener: l, Handler: &taggedHandler{inner: s.handler, transport: "tcp"}}

	s.track(udp, tcp)
	s.serve(udp, "udp", addr)
	s.serve(tcp, "tcp", addr)
	return nil
}

func (s *Server) listenDoT() error {
	if s.cfg.DotListen == "" {
		return nil
	}
	if s.cfg.TLSCert == "" || s.cfg.TLSKey == "" {
		return fmt.Errorf("dot_listen set but tls_cert/tls_key missing")
	}

	cert, err := tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey)
	if err != nil {
		return fmt.Errorf("load TLS keypair: %w", err)
	}

	l, err := tls.Listen("tcp", s.cfg.DotListen, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		return fmt.Errorf("bind dot %s: %w", s.cfg.DotListen, err)
	}

	dot := &dns.Server{Listener: l, Net: "tcp-tls", Handler: &taggedHandler{inner: s.handler, transport: "tcp-tls"}}
	s.track(dot)
	s.serve(dot, "tcp-tls", s.cfg.DotListen)
	return nil
}

func (s *Server) track(servers ...*dns.Server) {
	s.mu.Lock()
	s.servers = append(s.servers, servers...)
	s.mu.Unlock()
}

func (s *Server) trackHTTP(srv *http.Server) {
	s.mu.Lock()
	s.httpServers = append(s.httpServers, srv)
	s.mu.Unlock()
}

func (s *Server) serve(srv *dns.Server, proto, addr string) {
	go func() {
		slog.Info("northstar: listener started", "net", proto, "addr", addr)
		if err := srv.ActivateAndServe(); err != nil {
			slog.Error("northstar: listener stopped", "net", proto, "addr", addr, "error", err)
		}
	}()
}
