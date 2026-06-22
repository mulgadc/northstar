package server

import (
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoHResponseWriter exercises the dns.ResponseWriter adapter used to bridge
// HTTP requests into the shared dns.Handler.
func TestDoHResponseWriter(t *testing.T) {
	w := &dohResponseWriter{remote: "203.0.113.4:9999"}

	// RemoteAddr resolves the HTTP client address as a TCP address.
	assert.Equal(t, "203.0.113.4:9999", w.RemoteAddr().String())
	assert.NotNil(t, w.LocalAddr())

	// Write accepts a packed DNS message and decodes it.
	m := new(dns.Msg)
	m.SetQuestion("example.org.", dns.TypeA)
	wire, err := m.Pack()
	require.NoError(t, err)
	n, err := w.Write(wire)
	require.NoError(t, err)
	assert.Equal(t, len(wire), n)
	require.NotNil(t, w.msg)

	// Write rejects malformed input.
	_, err = w.Write([]byte{0x00})
	assert.Error(t, err)

	// No-op lifecycle methods are safe to call.
	assert.NoError(t, w.Close())
	assert.NoError(t, w.TsigStatus())
	w.TsigTimersOnly(true)
	w.Hijack()
}

// TestDoHRemoteAddrFallback verifies RemoteAddr falls back to loopback when the
// HTTP remote address cannot be parsed.
func TestDoHRemoteAddrFallback(t *testing.T) {
	w := &dohResponseWriter{remote: "not-an-address"}
	assert.NotNil(t, w.RemoteAddr())
}
