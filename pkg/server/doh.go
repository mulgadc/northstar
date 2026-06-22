package server

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/miekg/dns"
)

const (
	dohPath        = "/dns-query"
	dohContentType = "application/dns-message"
	dohMaxBody     = 65535
)

// listenDoH binds the DNS-over-HTTPS (RFC 8484) listener when configured. It
// reuses the same dns.Handler as the UDP/TCP/DoT paths via a capturing
// ResponseWriter, so zone lookups and recursion behave identically.
func (s *Server) listenDoH() error {
	if s.cfg.DohListen == "" {
		return nil
	}
	if s.cfg.TLSCert == "" || s.cfg.TLSKey == "" {
		return fmt.Errorf("doh_listen set but tls_cert/tls_key missing")
	}

	cert, err := tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey)
	if err != nil {
		return fmt.Errorf("load TLS keypair: %w", err)
	}

	l, err := tls.Listen("tcp", s.cfg.DohListen, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		return fmt.Errorf("bind doh %s: %w", s.cfg.DohListen, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(dohPath, s.serveDoH)
	httpSrv := &http.Server{Handler: mux, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}

	s.trackHTTP(httpSrv)
	go func() {
		slog.Info("northstar: listener started", "net", "doh", "addr", s.cfg.DohListen)
		if err := httpSrv.Serve(l); err != nil && err != http.ErrServerClosed {
			slog.Error("northstar: listener stopped", "net", "doh", "addr", s.cfg.DohListen, "error", err)
		}
	}()
	return nil
}

// serveDoH handles both GET (?dns=base64url) and POST (application/dns-message)
// wire-format requests per RFC 8484.
func (s *Server) serveDoH(w http.ResponseWriter, r *http.Request) {
	var wire []byte
	var err error

	switch r.Method {
	case http.MethodGet:
		b64 := r.URL.Query().Get("dns")
		if b64 == "" {
			http.Error(w, "missing dns parameter", http.StatusBadRequest)
			return
		}
		wire, err = base64.RawURLEncoding.DecodeString(b64)
	case http.MethodPost:
		if r.Header.Get("Content-Type") != dohContentType {
			http.Error(w, "unsupported content-type", http.StatusUnsupportedMediaType)
			return
		}
		wire, err = io.ReadAll(io.LimitReader(r.Body, dohMaxBody))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err != nil || len(wire) == 0 {
		http.Error(w, "invalid dns message", http.StatusBadRequest)
		return
	}

	req := new(dns.Msg)
	if err := req.Unpack(wire); err != nil {
		http.Error(w, "malformed dns message", http.StatusBadRequest)
		return
	}

	dw := &dohResponseWriter{remote: r.RemoteAddr}
	s.handler.ServeDNS(dw, req)
	if dw.msg == nil {
		http.Error(w, "no response", http.StatusInternalServerError)
		return
	}

	out, err := dw.msg.Pack()
	if err != nil {
		http.Error(w, "failed to pack response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", dohContentType)
	if _, err := w.Write(out); err != nil {
		slog.Debug("failed to write DoH response", "error", err)
	}
}

// dohResponseWriter adapts an HTTP request to dns.ResponseWriter, capturing the
// reply for serialization back over HTTP. RemoteAddr reports a TCP address so
// the handler treats DoH as a stream transport (no UDP truncation).
type dohResponseWriter struct {
	remote string
	msg    *dns.Msg
}

var _ dns.ResponseWriter = (*dohResponseWriter)(nil)

func (d *dohResponseWriter) WriteMsg(m *dns.Msg) error { d.msg = m; return nil }

func (d *dohResponseWriter) RemoteAddr() net.Addr {
	if addr, err := net.ResolveTCPAddr("tcp", d.remote); err == nil {
		return addr
	}
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (d *dohResponseWriter) LocalAddr() net.Addr { return &net.TCPAddr{} }
func (d *dohResponseWriter) Write(b []byte) (int, error) {
	m := new(dns.Msg)
	if err := m.Unpack(b); err != nil {
		return 0, err
	}
	d.msg = m
	return len(b), nil
}
func (d *dohResponseWriter) Close() error        { return nil }
func (d *dohResponseWriter) TsigStatus() error   { return nil }
func (d *dohResponseWriter) TsigTimersOnly(bool) {}
func (d *dohResponseWriter) Hijack()             {}
