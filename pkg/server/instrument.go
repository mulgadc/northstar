package server

import "github.com/miekg/dns"

// taggedHandler wraps the shared backend.Handler with the transport of the
// listener it is bound to. dns.Handler's ServeDNS gives the handler no way to
// tell DoT from plain TCP (both present as *net.TCPAddr), so each listener
// tags its ResponseWriter instead.
type taggedHandler struct {
	inner     dns.Handler
	transport string
}

var _ dns.Handler = (*taggedHandler)(nil)

func (t *taggedHandler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	t.inner.ServeDNS(&taggedResponseWriter{ResponseWriter: w, transport: t.transport}, r)
}

// taggedResponseWriter carries the listener's transport alongside the
// underlying dns.ResponseWriter so backend.Handler can read it via the
// Transport() string assertion.
type taggedResponseWriter struct {
	dns.ResponseWriter

	transport string
}

func (t *taggedResponseWriter) Transport() string { return t.transport }
