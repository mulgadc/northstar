package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/mulgadc/northstar/pkg/config"
	"github.com/mulgadc/northstar/pkg/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// genCert writes a self-signed cert/key pair to dir and returns their paths.
func genCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))
	return certPath, keyPath
}

// fakeUpstream runs an in-process authoritative DNS server answering a single
// name, simulating a public recursive resolver for recursion tests.
func fakeUpstream(t *testing.T, answerName, answerIP string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)

	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		if len(r.Question) > 0 && r.Question[0].Name == dns.Fqdn(answerName) {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: dns.Fqdn(answerName), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP(answerIP),
			})
		} else {
			m.SetRcode(r, dns.RcodeNameError)
		}
		_ = w.WriteMsg(m)
	})

	ds := &dns.Server{PacketConn: pc, Handler: handler}
	go func() { _ = ds.ActivateAndServe() }()
	t.Cleanup(func() { _ = ds.Shutdown() })
	return pc.LocalAddr().String()
}

// startServer builds a northstar server with a local zone, the given upstream
// nameservers, and TLS listeners for DoT/DoH. It returns the server and bind
// addresses.
func startServer(t *testing.T, dir string, upstream []string) (srv *server.Server, udpAddr, dotAddr, dohAddr string) {
	t.Helper()
	certPath, keyPath := genCert(t, dir)
	udpAddr = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	dotAddr = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	dohAddr = fmt.Sprintf("127.0.0.1:%d", freePort(t))

	srv, err := server.NewServer(config.ServerConfig{
		Listen:       udpAddr,
		DotListen:    dotAddr,
		DohListen:    dohAddr,
		TLSCert:      certPath,
		TLSKey:       keyPath,
		ZoneDir:      dir,
		SyncInterval: 1,
		Upstream:     config.UpstreamConfig{Nameservers: upstream},
	})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	time.Sleep(50 * time.Millisecond)
	return srv, udpAddr, dotAddr, dohAddr
}

// writeZoneWithSub writes a zone file with an apex record plus a subdomain
// record (e.g. "api" → 10.0.0.30 under "third.spx3.net").
func writeZoneWithSub(t *testing.T, dir, domain, apexAddr, sub, subAddr string) {
	t.Helper()
	body := fmt.Sprintf(`version = 1.0
[domain]
domain = "%s"
modified = 2022-05-27T07:32:00Z
[defaults]
ttl = 60
type = 1
class = 1
[[records]]
domain = ""
address = "%s"
[[records]]
domain = "%s."
address = "%s"
`, domain, apexAddr, sub, subAddr)
	require.NoError(t, os.WriteFile(filepath.Join(dir, domain+".toml"), []byte(body), 0o600))
}

// queryDoH sends a wire-format query over DNS-over-HTTPS (POST) and returns the
// decoded response.
func queryDoH(t *testing.T, addr, name string, qtype uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	wire, err := m.Pack()
	require.NoError(t, err)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	url := "https://" + addr + "/dns-query"
	resp, err := client.Post(url, "application/dns-message", bytes.NewReader(wire))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := new(dns.Msg)
	require.NoError(t, out.Unpack(body))
	return out
}

// queryDoHGet sends a GET DoH query with the base64url-encoded dns parameter.
func queryDoHGet(t *testing.T, addr, name string, qtype uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	wire, err := m.Pack()
	require.NoError(t, err)
	b64 := base64.RawURLEncoding.EncodeToString(wire)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := client.Get("https://" + addr + "/dns-query?dns=" + b64)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := new(dns.Msg)
	require.NoError(t, out.Unpack(body))
	return out
}

// queryNet sends a query over a specific transport ("udp", "tcp", "tcp-tls").
func queryNet(t *testing.T, network, addr, name string, qtype uint16) *dns.Msg {
	t.Helper()
	c := dns.Client{Net: network}
	if network == "tcp-tls" {
		c.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	r, _, err := c.Exchange(m, addr)
	require.NoError(t, err)
	return r
}

func assertA(t *testing.T, r *dns.Msg, wantIP string) {
	t.Helper()
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)
	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, wantIP, a.A.String())
}

// TestTransportsLocalResolve verifies local zone resolution across every
// transport: UDP, TCP, DoT (tcp-tls) and DoH (GET + POST).
func TestTransportsLocalResolve(t *testing.T) {
	dir := t.TempDir()
	writeZone(t, dir, "host.spx3.net", "10.0.0.5")

	_, udpAddr, dotAddr, dohAddr := startServer(t, dir, nil)

	assertA(t, queryNet(t, "udp", udpAddr, "host.spx3.net", dns.TypeA), "10.0.0.5")
	assertA(t, queryNet(t, "tcp", udpAddr, "host.spx3.net", dns.TypeA), "10.0.0.5")
	assertA(t, queryNet(t, "tcp-tls", dotAddr, "host.spx3.net", dns.TypeA), "10.0.0.5")
	assertA(t, queryDoH(t, dohAddr, "host.spx3.net", dns.TypeA), "10.0.0.5")
	assertA(t, queryDoHGet(t, dohAddr, "host.spx3.net", dns.TypeA), "10.0.0.5")
}

// TestNewDomainAndSubdomainResolve verifies that a newly added zone and a
// subdomain added to an existing zone resolve after Reload.
func TestNewDomainAndSubdomainResolve(t *testing.T) {
	dir := t.TempDir()
	writeZone(t, dir, "first.spx3.net", "10.0.0.1")
	srv, udpAddr, _, _ := startServer(t, dir, nil)

	assertA(t, queryNet(t, "udp", udpAddr, "first.spx3.net", dns.TypeA), "10.0.0.1")

	// Add a brand-new zone file and a subdomain record, then reload.
	writeZone(t, dir, "second.spx3.net", "10.0.0.2")
	writeZoneWithSub(t, dir, "third.spx3.net", "10.0.0.3", "api", "10.0.0.30")
	require.NoError(t, srv.Reload())

	assertA(t, queryNet(t, "udp", udpAddr, "second.spx3.net", dns.TypeA), "10.0.0.2")
	assertA(t, queryNet(t, "udp", udpAddr, "third.spx3.net", dns.TypeA), "10.0.0.3")
	assertA(t, queryNet(t, "udp", udpAddr, "api.third.spx3.net", dns.TypeA), "10.0.0.30")
}

// TestRecursionToUpstream verifies that a non-authoritative name is forwarded
// to the configured upstream resolver (an in-process fake), over every
// transport.
func TestRecursionToUpstream(t *testing.T) {
	dir := t.TempDir()
	writeZone(t, dir, "local.spx3.net", "10.0.0.9")
	upstreamAddr := fakeUpstream(t, "cnn.com", "151.101.1.67")

	_, udpAddr, dotAddr, dohAddr := startServer(t, dir, []string{upstreamAddr})

	assertA(t, queryNet(t, "udp", udpAddr, "cnn.com", dns.TypeA), "151.101.1.67")
	assertA(t, queryNet(t, "tcp", udpAddr, "cnn.com", dns.TypeA), "151.101.1.67")
	assertA(t, queryNet(t, "tcp-tls", dotAddr, "cnn.com", dns.TypeA), "151.101.1.67")
	assertA(t, queryDoH(t, dohAddr, "cnn.com", dns.TypeA), "151.101.1.67")

	// Local names still resolve authoritatively alongside recursion.
	r := queryNet(t, "udp", udpAddr, "local.spx3.net", dns.TypeA)
	assertA(t, r, "10.0.0.9")
	assert.True(t, r.Authoritative)
}

// TestRecursionRefusedNoUpstream verifies that with no upstream configured a
// non-authoritative query is refused (air-gap safe).
func TestRecursionRefusedNoUpstream(t *testing.T) {
	dir := t.TempDir()
	writeZone(t, dir, "local.spx3.net", "10.0.0.9")
	_, udpAddr, _, _ := startServer(t, dir, nil)

	r := queryNet(t, "udp", udpAddr, "cnn.com", dns.TypeA)
	assert.Equal(t, dns.RcodeRefused, r.Rcode)
}

// TestDoHErrors verifies the DoH endpoint rejects malformed requests with the
// correct HTTP status codes.
func TestDoHErrors(t *testing.T) {
	dir := t.TempDir()
	writeZone(t, dir, "host.spx3.net", "10.0.0.5")
	_, _, _, dohAddr := startServer(t, dir, nil)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	base := "https://" + dohAddr + "/dns-query"

	// GET without the dns parameter → 400.
	resp, err := client.Get(base)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// GET with undecodable dns parameter → 400.
	resp, err = client.Get(base + "?dns=!!!notbase64!!!")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// POST with the wrong content-type → 415.
	resp, err = client.Post(base, "text/plain", bytes.NewReader([]byte("x")))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)

	// POST a body that is not a valid DNS message → 400.
	resp, err = client.Post(base, "application/dns-message", bytes.NewReader([]byte{0x01, 0x02}))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Unsupported method → 405.
	req, err := http.NewRequest(http.MethodPut, base, nil)
	require.NoError(t, err)
	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}
