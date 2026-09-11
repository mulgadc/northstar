package backend_test

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DNS names are case-insensitive (RFC 1035 §2.3.3), and Google Public DNS
// relies on that by randomising the case of every outgoing query as an
// anti-spoofing measure (DNS 0x20). Northstar matched names case-sensitively,
// so every 0x20 query was REFUSED and spx3.net did not resolve for roughly a
// third of the public resolver market.
//
// There are three separate failure modes, and they need separate tests: a
// mixed-case zone missed FindZone and was REFUSED, a mixed-case label under a
// lowercase zone found the zone but missed the record and returned NXDOMAIN,
// and a fix that normalised the response name as well would satisfy neither
// because 0x20 discards any reply whose case does not match the question.

// spellings returns the same name in the cases a resolver might send it.
func spellings(name string) map[string]string {
	return map[string]string{
		"lower":      strings.ToLower(name),
		"upper":      strings.ToUpper(name),
		"mixed":      mixCase(name),
		"firstUpper": strings.ToUpper(name[:1]) + name[1:],
	}
}

// mixCase upper-cases every second letter, so the result differs from both
// strings.ToLower and strings.ToUpper.
func mixCase(name string) string {
	out := []rune(strings.ToLower(name))
	for i := range out {
		if i%2 == 0 {
			out[i] = []rune(strings.ToUpper(string(out[i])))[0]
		}
	}
	return string(out)
}

// randomiseCase flips each letter independently, which is what Google Public
// DNS does to the question name on the wire.
func randomiseCase(name string, rng *rand.Rand) string {
	out := []rune(strings.ToLower(name))
	for i, r := range out {
		if r >= 'a' && r <= 'z' && rng.IntN(2) == 0 {
			out[i] = r - 32
		}
	}
	return string(out)
}

func askServer(t *testing.T, name string, qtype uint16) *dns.Msg {
	t.Helper()
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn(name), qtype)
	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	return r
}

// TestCaseInsensitiveZoneMatch is the reported bug: SPX3.NET was REFUSED while
// spx3.net was answered.
func TestCaseInsensitiveZoneMatch(t *testing.T) {
	want := askServer(t, "hello_a.net", dns.TypeA)
	require.Equal(t, dns.RcodeSuccess, want.Rcode)
	require.NotEmpty(t, want.Answer)

	for label, name := range spellings("hello_a.net") {
		t.Run(label, func(t *testing.T) {
			got := askServer(t, name, dns.TypeA)

			assert.Equal(t, dns.RcodeSuccess, got.Rcode,
				"%q must not be REFUSED: DNS names are case-insensitive", name)
			assert.True(t, got.Authoritative, "%q must be answered authoritatively", name)
			require.Len(t, got.Answer, len(want.Answer))

			a, ok := got.Answer[0].(*dns.A)
			require.True(t, ok)
			assert.Equal(t, "203.100.1.1", a.A.String())
		})
	}
}

// TestCaseInsensitiveLabelMatch guards the regression that a partial fix
// introduces. Normalising only the zone lookup finds the zone, misses the
// record, and returns NXDOMAIN — which is worse than the REFUSED it replaces,
// because resolvers cache an authoritative negative for the SOA minimum while
// REFUSED is a soft failure they retry against another server.
func TestCaseInsensitiveLabelMatch(t *testing.T) {
	for label, name := range spellings("ns1") {
		t.Run(label, func(t *testing.T) {
			// Zone stays lowercase; only the leftmost label varies.
			got := askServer(t, name+".hello_a.net", dns.TypeA)

			require.NotEqual(t, dns.RcodeNameError, got.Rcode,
				"%q.hello_a.net returned NXDOMAIN: the zone matched but the record lookup did not", name)
			assert.Equal(t, dns.RcodeSuccess, got.Rcode)
			require.NotEmpty(t, got.Answer)

			a, ok := got.Answer[0].(*dns.A)
			require.True(t, ok)
			assert.Equal(t, "203.100.1.10", a.A.String())
		})
	}
}

// TestCaseInsensitiveAcrossRecordTypes covers the whole qtype switch, since the
// live server refused every type identically and a per-type handler could
// reintroduce the bug for one of them.
func TestCaseInsensitiveAcrossRecordTypes(t *testing.T) {
	for _, qtype := range []uint16{
		dns.TypeA, dns.TypeAAAA, dns.TypeNS, dns.TypeMX, dns.TypeTXT, dns.TypeSOA,
	} {
		t.Run(dns.TypeToString[qtype], func(t *testing.T) {
			lower := askServer(t, "hello_a.net", qtype)
			mixed := askServer(t, mixCase("hello_a.net"), qtype)

			assert.Equal(t, lower.Rcode, mixed.Rcode)
			assert.Equal(t, lower.Authoritative, mixed.Authoritative)
			require.Len(t, mixed.Answer, len(lower.Answer),
				"%s answer count differs between cases", dns.TypeToString[qtype])
		})
	}
}

// TestResponseEchoesQueryCase is the property that makes the fix usable. A
// 0x20 resolver compares the question it receives back against the one it sent
// and discards any mismatch as a spoofing attempt, so canonicalising the name
// in the answer would leave the domain just as unresolvable as before — while
// passing every other test in this file.
func TestResponseEchoesQueryCase(t *testing.T) {
	name := dns.Fqdn(mixCase("hello_a.net"))

	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(name, dns.TypeA)
	r, _, err := c.Exchange(&m, testNS)
	require.NoError(t, err)
	require.Equal(t, dns.RcodeSuccess, r.Rcode)

	require.NotEmpty(t, r.Question)
	assert.Equal(t, name, r.Question[0].Name,
		"the question section must echo the client's case exactly")

	require.NotEmpty(t, r.Answer)
	for _, rr := range r.Answer {
		assert.Equal(t, name, rr.Header().Name,
			"answer RR names must echo the client's case, not the stored case")
	}
}

// TestDNS0x20Resolves simulates what Google Public DNS actually sends: a fresh
// random case per query. Deterministically seeded so a failure reproduces.
func TestDNS0x20Resolves(t *testing.T) {
	rng := rand.New(rand.NewPCG(0x20, 0x20))

	for range 50 {
		name := dns.Fqdn(randomiseCase("host-1.hello_a.net", rng))

		c := dns.Client{}
		m := dns.Msg{}
		m.SetQuestion(name, dns.TypeA)
		r, _, err := c.Exchange(&m, testNS)
		require.NoError(t, err)

		require.Equal(t, dns.RcodeSuccess, r.Rcode, "0x20 query %q was not answered", name)
		require.NotEmpty(t, r.Answer, "0x20 query %q returned no answer", name)
		assert.Equal(t, name, r.Answer[0].Header().Name,
			"0x20 query %q: response case must match or the resolver discards it", name)
	}
}

// TestCaseInsensitiveWildcard covers wildcardFor, whose suffix test is a byte
// comparison that survived only because its two inputs happened to agree.
func TestCaseInsensitiveWildcard(t *testing.T) {
	for label, name := range spellings("random.wildcard.net") {
		t.Run(label, func(t *testing.T) {
			got := askServer(t, name, dns.TypeA)

			assert.Equal(t, dns.RcodeSuccess, got.Rcode)
			require.NotEmpty(t, got.Answer)

			a, ok := got.Answer[0].(*dns.A)
			require.True(t, ok)
			assert.Equal(t, "10.10.10.99", a.A.String())
		})
	}
}

// TestCaseInsensitiveNODATA covers NameExists, which picks NODATA over
// NXDOMAIN. Case-sensitively it reports a name that plainly exists as absent,
// and NXDOMAIN is the answer resolvers cache hardest.
func TestCaseInsensitiveNODATA(t *testing.T) {
	// ns1.hello_a.net exists as an A record but has no MX.
	lower := askServer(t, "ns1.hello_a.net", dns.TypeMX)
	require.Equal(t, dns.RcodeSuccess, lower.Rcode)
	assert.Empty(t, lower.Answer)

	mixed := askServer(t, mixCase("ns1.hello_a.net"), dns.TypeMX)
	assert.Equal(t, dns.RcodeSuccess, mixed.Rcode,
		"a name that exists with another type must be NODATA, not NXDOMAIN")
	assert.Empty(t, mixed.Answer)
}

// TestCaseInsensitiveNXDOMAIN checks the fix did not go the other way and make
// everything under a zone resolve.
func TestCaseInsensitiveNXDOMAIN(t *testing.T) {
	for label, name := range spellings("definitely-not-here.hello_a.net") {
		t.Run(label, func(t *testing.T) {
			got := askServer(t, name, dns.TypeA)
			assert.Equal(t, dns.RcodeNameError, got.Rcode)
			assert.True(t, got.Authoritative)
		})
	}
}

// TestCaseInsensitiveRefusedStaysRefused checks a foreign name is still not
// ours in any case. Over-normalising into authority would be a worse bug than
// the one being fixed.
func TestCaseInsensitiveRefusedStaysRefused(t *testing.T) {
	for label, name := range spellings("totally-unknown-domain.xyz") {
		t.Run(label, func(t *testing.T) {
			got := askServer(t, name, dns.TypeA)
			assert.Equal(t, dns.RcodeRefused, got.Rcode)
		})
	}
}

// TestCaseInsensitiveNSAuthorityAndGlue covers addNSAuthority and lookupExtra,
// which build their keys from the zone name rather than the query. Both went
// silently empty rather than failing, so only the section contents catch it.
func TestCaseInsensitiveNSAuthorityAndGlue(t *testing.T) {
	lower := askServer(t, "hello_a.net", dns.TypeNS)
	require.Equal(t, dns.RcodeSuccess, lower.Rcode)
	require.NotEmpty(t, lower.Answer)
	require.NotEmpty(t, lower.Extra, "lowercase NS query should carry glue")

	mixed := askServer(t, mixCase("hello_a.net"), dns.TypeNS)
	require.Equal(t, dns.RcodeSuccess, mixed.Rcode)
	assert.Len(t, mixed.Answer, len(lower.Answer))
	assert.Len(t, mixed.Extra, len(lower.Extra),
		"glue is looked up by zone name and silently vanishes when the case differs")
}

// TestCaseInsensitiveSOASerial covers the SOA path, which falls back to a
// synthesised name and serial 0 on a keying miss rather than erroring.
func TestCaseInsensitiveSOASerial(t *testing.T) {
	lower := askServer(t, "hello_a.net", dns.TypeSOA)
	require.NotEmpty(t, lower.Answer)
	lowerSOA, ok := lower.Answer[0].(*dns.SOA)
	require.True(t, ok)

	mixed := askServer(t, mixCase("hello_a.net"), dns.TypeSOA)
	require.NotEmpty(t, mixed.Answer)
	mixedSOA, ok := mixed.Answer[0].(*dns.SOA)
	require.True(t, ok)

	assert.Equal(t, lowerSOA.Ns, mixedSOA.Ns)
	assert.Equal(t, lowerSOA.Serial, mixedSOA.Serial,
		"a keying miss yields serial 0, which would make every zone look unchanged")
	assert.NotZero(t, mixedSOA.Serial)
}
