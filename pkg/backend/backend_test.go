package backend_test

import (
	"crypto/md5"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/mulgadc/northstar/pkg/backend"
	"github.com/mulgadc/northstar/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testHost = "127.0.0.1"
	testPort = 5354
)

var testNS string

func init() {
	testNS = fmt.Sprintf("%s:%d", testHost, testPort)

	setupTestConfig()
	startTestServer()
	// Give server time to bind
	time.Sleep(50 * time.Millisecond)
}

func setupTestConfig() {
	_ = os.Mkdir("./testconfig", 0755)

	var md5str string
	iprange := 1

	for i := 'a'; i <= 'z'; i++ {
		md5str = fmt.Sprintf("site-verification-hello_%c.net", i)
		md5str = fmt.Sprintf("%x", md5.Sum([]byte(md5str)))

		filename := fmt.Sprintf("testconfig/hello_%c.net.toml", i)

		output := `
# Domain configuration file in TOML format.
version = 1.0

# Domain settings
[domain]
domain = "hello_%c.net"
created = 2021-05-27T07:32:00Z
modified = 2022-05-27T07:32:00Z
soa = "ns1.hello_%c.net."
verified = true
active = true
ownerid = 10

# Default settings if not defined in each [[records]]
[defaults]
ttl = 3600
type = 1
class = 1

# Domain entry, one entry per record
[[records]]
domain = ""
address = "203.100.%d.1"

[[records]]
domain = "www."
address = "203.100.%d.1"

# NS records
[[records]]
domain = ""
type = 2
address = "ns1.hello_%c.net."

[[records]]
domain = ""
type = 2
address = "ns2.hello_%c.net."

# Glue records for NS
[[records]]
domain = "ns1."
address = "203.100.%d.10"

[[records]]
domain = "ns2."
address = "203.100.%d.11"
`

		record := fmt.Sprintf(output, i, i, iprange, iprange, i, i, iprange, iprange)

		for i2 := 1; i2 < 253; i2++ {
			output = `
[[records]]
domain = "host-%d."
address = "203.100.%d.%d"
		`
			record += fmt.Sprintf(output, i2, iprange, i2)
		}

		var spfips string
		preference := 10

		for i3 := 10; i3 <= 13; i3++ {
			output = `
[[records]]
domain = ""
type = 15
preference = %d
address = "host-%d.hello_%c.net."
					`
			record += fmt.Sprintf(output, preference, i3, i)
			spfips += fmt.Sprintf(" ip:203.100.%d.%d", iprange, i3)
			preference += 10
		}

		output = `
[[records]]
domain = ""
type = 16
address = "v=spf1%s mx a -all"

[[records]]
domain = ""
type = 16
address = "google-site-verification=%s"
		`

		record += fmt.Sprintf(output, spfips, md5str)

		// Add AAAA record
		output = `
[[records]]
domain = ""
type = 28
address = "2001:db8::%d"
`
		record += fmt.Sprintf(output, iprange)

		f, _ := os.Create(filename)
		defer f.Close()
		f.WriteString(record)

		iprange++
	}

	// Create SRV test zone
	srvZone := `
version = 1.0

[domain]
domain = "srvtest.net"
created = 2021-05-27T07:32:00Z
modified = 2022-05-27T07:32:00Z
soa = "ns1.srvtest.net."
verified = true
active = true
ownerid = 10

[defaults]
ttl = 300
type = 1
class = 1

[[records]]
domain = ""
address = "10.0.0.1"

[[records]]
domain = "_nats._tcp."
type = 33
priority = 10
weight = 0
port = 4222
address = "node1.srvtest.net."

[[records]]
domain = "_nats._tcp."
type = 33
priority = 20
weight = 0
port = 4222
address = "node2.srvtest.net."

[[records]]
domain = "node1."
address = "10.0.1.1"

[[records]]
domain = "node2."
address = "10.0.1.2"
`
	f, _ := os.Create("testconfig/srvtest.net.toml")
	f.WriteString(srvZone)
	f.Close()

	// Create CAA test zone
	caaZone := `
version = 1.0

[domain]
domain = "caatest.net"
created = 2021-05-27T07:32:00Z
modified = 2022-05-27T07:32:00Z
verified = true
active = true
ownerid = 10

[defaults]
ttl = 3600
type = 1
class = 1

[[records]]
domain = ""
address = "10.0.0.2"

[[records]]
domain = ""
type = 257
caa_flag = 0
caa_tag = "issue"
address = "letsencrypt.org"

[[records]]
domain = ""
type = 257
caa_flag = 0
caa_tag = "issuewild"
address = "letsencrypt.org"
`
	f, _ = os.Create("testconfig/caatest.net.toml")
	f.WriteString(caaZone)
	f.Close()

	// Create PTR test zone
	ptrZone := `
version = 1.0

[domain]
domain = "1.168.192.in-addr.arpa"
created = 2021-05-27T07:32:00Z
modified = 2022-05-27T07:32:00Z
verified = true
active = true
ownerid = 10

[defaults]
ttl = 3600
type = 12
class = 1

[[records]]
domain = "1."
type = 12
address = "host-1.example.net."

[[records]]
domain = "2."
type = 12
address = "host-2.example.net."
`
	f, _ = os.Create("testconfig/1.168.192.in-addr.arpa.toml")
	f.WriteString(ptrZone)
	f.Close()

	// Create wildcard test zone
	wildcardZone := `
version = 1.0

[domain]
domain = "wildcard.net"
created = 2021-05-27T07:32:00Z
modified = 2022-05-27T07:32:00Z
verified = true
active = true
ownerid = 10

[defaults]
ttl = 3600
type = 1
class = 1

[[records]]
domain = ""
address = "10.10.10.1"

[[records]]
domain = "*."
address = "10.10.10.99"

[[records]]
domain = "specific."
address = "10.10.10.50"
`
	f, _ = os.Create("testconfig/wildcard.net.toml")
	f.WriteString(wildcardZone)
	f.Close()
}

func startTestServer() {
	cfg, err := config.ReadZoneFiles("./testconfig/", nil)
	if err != nil {
		panic(err)
	}

	handler := &backend.Handler{
		Conf:     cfg,
		Upstream: backend.NewUpstream(nil),
	}

	// UDP server
	srvUDP := &dns.Server{Addr: fmt.Sprintf("%s:%d", testHost, testPort), Net: "udp", Handler: handler}
	go func() {
		if err := srvUDP.ListenAndServe(); err != nil {
			fmt.Printf("UDP server failed: %v\n", err)
		}
	}()

	// TCP server
	srvTCP := &dns.Server{Addr: fmt.Sprintf("%s:%d", testHost, testPort), Net: "tcp", Handler: handler}
	go func() {
		if err := srvTCP.ListenAndServe(); err != nil {
			fmt.Printf("TCP server failed: %v\n", err)
		}
	}()
}

// --- A Record Tests ---

func TestARecord(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	assert.True(t, r.Authoritative)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "203.100.1.1", a.A.String())
}

func TestARecordSubdomain(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("host-1.hello_a.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "203.100.1.1", a.A.String())
}

// --- AAAA Record Tests ---

func TestAAAARecord(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeAAAA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	aaaa, ok := r.Answer[0].(*dns.AAAA)
	require.True(t, ok)
	assert.Equal(t, "2001:db8::1", aaaa.AAAA.String())
}

// --- TCP Query Tests ---

func TestTCPQuery(t *testing.T) {
	c := dns.Client{Net: "tcp"}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "203.100.1.1", a.A.String())
}

// --- MX Record Tests ---

func TestMXRecord(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeMX)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)

	var mx []string
	var pref []uint16
	for _, k := range r.Answer {
		if key, ok := k.(*dns.MX); ok {
			mx = append(mx, key.Mx)
			pref = append(pref, key.Preference)
		}
	}

	assert.Len(t, mx, 4)
	if len(mx) > 0 {
		assert.Equal(t, "host-10.hello_a.net.", mx[0])
	}

	if len(pref) == 4 {
		assert.Equal(t, uint16(10), pref[0])
		assert.Equal(t, uint16(20), pref[1])
		assert.Equal(t, uint16(30), pref[2])
		assert.Equal(t, uint16(40), pref[3])
	}
}

// --- NS Record Tests ---

func TestNSRecord(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeNS)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)

	var nsRecords []string
	for _, k := range r.Answer {
		if key, ok := k.(*dns.NS); ok {
			nsRecords = append(nsRecords, key.Ns)
		}
	}

	assert.Len(t, nsRecords, 2)
	assert.Contains(t, nsRecords, "ns1.hello_a.net.")
	assert.Contains(t, nsRecords, "ns2.hello_a.net.")
}

func TestNSAuthority(t *testing.T) {
	// When querying for A record, authority section should contain NS records
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)

	var nsRecords []string
	for _, k := range r.Ns {
		if key, ok := k.(*dns.NS); ok {
			nsRecords = append(nsRecords, key.Ns)
		}
	}

	assert.GreaterOrEqual(t, len(nsRecords), 1, "Authority section should contain NS records")
}

// --- TXT Record Tests ---

func TestTXTRecord(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeTXT)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)

	var txt []string
	for _, k := range r.Answer {
		if key, ok := k.(*dns.TXT); ok {
			txt = append(txt, key.Txt[0])
		}
	}

	assert.Len(t, txt, 2)

	spfok := strings.HasPrefix(txt[0], "v=spf1 ip:203")
	assert.True(t, spfok)

	md5str := "site-verification-hello_a.net"
	md5str = fmt.Sprintf("%x", md5.Sum([]byte(md5str)))
	assert.Equal(t, fmt.Sprintf("google-site-verification=%s", md5str), txt[1])
}

// --- SOA Record Tests ---

func TestSOARecord(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeSOA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	soa, ok := r.Answer[0].(*dns.SOA)
	require.True(t, ok)
	assert.Equal(t, "ns1.hello_a.net.", soa.Ns)
	assert.Contains(t, soa.Mbox, "hostmaster.")
	// Serial should be based on Modified timestamp (2022-05-27), not time.Now()
	assert.Positive(t, soa.Serial)
}

// --- SRV Record Tests ---

func TestSRVRecord(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("_nats._tcp.srvtest.net"), dns.TypeSRV)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)

	var srvRecords []*dns.SRV
	for _, k := range r.Answer {
		if key, ok := k.(*dns.SRV); ok {
			srvRecords = append(srvRecords, key)
		}
	}

	assert.Len(t, srvRecords, 2)

	if len(srvRecords) >= 2 {
		assert.Equal(t, uint16(10), srvRecords[0].Priority)
		assert.Equal(t, uint16(4222), srvRecords[0].Port)
		assert.Equal(t, "node1.srvtest.net.", srvRecords[0].Target)

		assert.Equal(t, uint16(20), srvRecords[1].Priority)
		assert.Equal(t, uint16(4222), srvRecords[1].Port)
		assert.Equal(t, "node2.srvtest.net.", srvRecords[1].Target)
	}
}

// --- CAA Record Tests ---

func TestCAARecord(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("caatest.net"), dns.TypeCAA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)

	var caaRecords []*dns.CAA
	for _, k := range r.Answer {
		if key, ok := k.(*dns.CAA); ok {
			caaRecords = append(caaRecords, key)
		}
	}

	assert.Len(t, caaRecords, 2)
	if len(caaRecords) >= 2 {
		assert.Equal(t, "issue", caaRecords[0].Tag)
		assert.Equal(t, "letsencrypt.org", caaRecords[0].Value)
		assert.Equal(t, "issuewild", caaRecords[1].Tag)
	}
}

// --- PTR Record Tests ---

func TestPTRRecord(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("1.1.168.192.in-addr.arpa"), dns.TypePTR)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	ptr, ok := r.Answer[0].(*dns.PTR)
	require.True(t, ok)
	assert.Equal(t, "host-1.example.net.", ptr.Ptr)
}

// --- Response Code Tests ---

func TestNXDOMAIN(t *testing.T) {
	// Non-existent name under our zone → NXDOMAIN
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("nonexistent.hello_a.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeNameError, r.Rcode) // NXDOMAIN
	assert.True(t, r.Authoritative)
	assert.Empty(t, r.Answer)
	// Should have SOA in authority section
	assert.NotEmpty(t, r.Ns)
	_, ok := r.Ns[0].(*dns.SOA)
	assert.True(t, ok, "Authority section should contain SOA for NXDOMAIN")
}

func TestNODATA(t *testing.T) {
	// Existing name, but no records of queried type → NOERROR with empty answer
	c := dns.Client{}
	m := dns.Msg{}
	// hello_a.net has A records but query for SRV which it doesn't have
	m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeSRV)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	// Should be NOERROR (NODATA), not REFUSED
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	assert.True(t, r.Authoritative)
	assert.Empty(t, r.Answer)
}

func TestREFUSED(t *testing.T) {
	// Domain not in any of our zones → REFUSED
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("totally-unknown-domain.xyz"), dns.TypeA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeRefused, r.Rcode)
}

// --- EDNS0 Tests ---

func TestEDNS0(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeA)
	m.SetEdns0(4096, false)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)

	// Response should contain EDNS0 OPT record
	opt := r.IsEdns0()
	assert.NotNil(t, opt, "Response should contain EDNS0 OPT record")
	if opt != nil {
		assert.Equal(t, uint16(4096), opt.UDPSize())
	}
}

// --- Wildcard Tests ---

func TestWildcard(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	// random.wildcard.net should match *.wildcard.net
	m.SetQuestion(dns.Fqdn("random.wildcard.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "10.10.10.99", a.A.String())
}

func TestWildcardExactMatch(t *testing.T) {
	// specific.wildcard.net should match the exact record, not wildcard
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("specific.wildcard.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "10.10.10.50", a.A.String())
}

// --- Concurrency Test ---

func TestConcurrency(t *testing.T) {
	var wg sync.WaitGroup
	errors := make(chan error, 100)

	for i := range 100 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c := dns.Client{}
			m := dns.Msg{}

			letter := rune('a' + (idx % 26))
			m.SetQuestion(dns.Fqdn(fmt.Sprintf("hello_%c.net", letter)), dns.TypeA)

			r, _, err := c.Exchange(&m, testNS)
			if err != nil {
				errors <- err
				return
			}
			if r.Rcode != dns.RcodeSuccess {
				errors <- fmt.Errorf("query %d: got rcode %d", idx, r.Rcode)
				return
			}
			if len(r.Answer) == 0 {
				errors <- fmt.Errorf("query %d: empty answer", idx)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}

// --- Full 26-domain Suite (preserved from original tests) ---

func TestAllDomains(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}

	iprange := 1

	for i := 'a'; i <= 'z'; i++ {
		// Test A records
		m.SetQuestion(dns.Fqdn(fmt.Sprintf("hello_%c.net", i)), dns.TypeA)
		r, _, err := c.Exchange(&m, testNS)
		require.NoError(t, err)
		assert.Equal(t, dns.RcodeSuccess, r.Rcode)
		require.NotEmpty(t, r.Answer)

		for _, k := range r.Answer {
			if key, ok := k.(*dns.A); ok {
				assert.Equal(t, fmt.Sprintf("203.100.%d.1", iprange), key.A.String())
			}
		}

		// Test subdomains
		for i2 := 1; i2 < 10; i2++ {
			m.SetQuestion(dns.Fqdn(fmt.Sprintf("host-%d.hello_%c.net", i2, i)), dns.TypeA)
			r, _, err := c.Exchange(&m, testNS)
			require.NoError(t, err)
			assert.Equal(t, dns.RcodeSuccess, r.Rcode)

			for _, k := range r.Answer {
				if key, ok := k.(*dns.A); ok {
					assert.Equal(t, fmt.Sprintf("203.100.%d.%d", iprange, i2), key.A.String())
				}
			}
		}

		// Test MX
		m.SetQuestion(dns.Fqdn(fmt.Sprintf("hello_%c.net", i)), dns.TypeMX)
		r, _, err = c.Exchange(&m, testNS)
		require.NoError(t, err)
		assert.Equal(t, dns.RcodeSuccess, r.Rcode)

		var mx []string
		for _, k := range r.Answer {
			if key, ok := k.(*dns.MX); ok {
				mx = append(mx, key.Mx)
			}
		}
		assert.Len(t, mx, 4)

		// Test TXT
		m.SetQuestion(dns.Fqdn(fmt.Sprintf("hello_%c.net", i)), dns.TypeTXT)
		r, _, err = c.Exchange(&m, testNS)
		require.NoError(t, err)
		assert.Equal(t, dns.RcodeSuccess, r.Rcode)

		var txt []string
		for _, k := range r.Answer {
			if key, ok := k.(*dns.TXT); ok {
				txt = append(txt, key.Txt[0])
			}
		}
		assert.Len(t, txt, 2)

		iprange++
	}
}

// --- Cleanup ---

func TestCleanup(t *testing.T) {
	err := os.RemoveAll("./testconfig")
	assert.NoError(t, err)
}

// --- Benchmarks ---

func BenchmarkDNSQueryA(b *testing.B) {
	c := dns.Client{}
	m := dns.Msg{}

	for n := 0; n < b.N; n++ {
		m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeA)
		r, _, err := c.Exchange(&m, testNS)
		if err != nil || r.Rcode != dns.RcodeSuccess {
			b.Fatal("query failed")
		}
	}
}

func BenchmarkDNSQueryTXT(b *testing.B) {
	c := dns.Client{}
	m := dns.Msg{}

	for n := 0; n < b.N; n++ {
		m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeTXT)
		r, _, err := c.Exchange(&m, testNS)
		if err != nil || r.Rcode != dns.RcodeSuccess {
			b.Fatal("query failed")
		}
	}
}

func BenchmarkDNSQueryMX(b *testing.B) {
	c := dns.Client{}
	m := dns.Msg{}

	for n := 0; n < b.N; n++ {
		m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeMX)
		r, _, err := c.Exchange(&m, testNS)
		if err != nil || r.Rcode != dns.RcodeSuccess {
			b.Fatal("query failed")
		}
	}
}

func BenchmarkDNSQueryTCP(b *testing.B) {
	c := dns.Client{Net: "tcp"}
	m := dns.Msg{}

	for n := 0; n < b.N; n++ {
		m.SetQuestion(dns.Fqdn("hello_a.net"), dns.TypeA)
		r, _, err := c.Exchange(&m, testNS)
		if err != nil || r.Rcode != dns.RcodeSuccess {
			b.Fatal("query failed")
		}
	}
}
