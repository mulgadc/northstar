package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	northstarUDP = "127.0.0.1:5553"
	northstarTCP = "127.0.0.1:5553"
	northstarDoT = "127.0.0.1:8853"
	predastore   = "https://127.0.0.1:9443"
	bucketName   = "dns-zones"
	accessKey    = "AKIAIOSFODNN7EXAMPLE"
	secretKey    = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	awsRegion    = "us-east-1"
	testCAFile   = "testdata/certs/ca.pem"
)

func TestMain(m *testing.M) {
	if os.Getenv("NORTHSTAR_E2E") == "" {
		fmt.Println("Skipping E2E tests (set NORTHSTAR_E2E=1 to run)")
		os.Exit(0)
	}

	// Upload zone files to predastore S3
	if err := uploadZoneFiles(); err != nil {
		fmt.Printf("Failed to upload zone files: %v\n", err)
		os.Exit(1)
	}

	// Wait for northstar to sync both zone files from S3.
	fmt.Println("Waiting for northstar to sync zone files...")
	for _, domain := range []string{"e2etest.net", "nxdomain.test"} {
		if err := waitForDNS(domain, 30*time.Second); err != nil {
			fmt.Printf("Northstar DNS not ready: %v\n", err)
			os.Exit(1)
		}
	}

	os.Exit(m.Run())
}

func s3Client() (*s3.Client, error) {
	caPEM, err := os.ReadFile(testCAFile)
	if err != nil {
		return nil, fmt.Errorf("reading E2E CA certificate: %w", err)
	}

	block, rest := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("parsing E2E CA certificate %s: expected a CERTIFICATE PEM block", testCAFile)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("parsing E2E CA certificate %s: unexpected trailing content", testCAFile)
	}

	caCertificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing E2E CA certificate %s: %w", testCAFile, err)
	}
	if !caCertificate.BasicConstraintsValid || !caCertificate.IsCA {
		return nil, fmt.Errorf("validating E2E CA certificate %s: certificate is not a CA", testCAFile)
	}

	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(caCertificate)

	return s3.New(s3.Options{
		Region:       awsRegion,
		BaseEndpoint: aws.String(predastore),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					RootCAs:    rootCAs,
				},
			},
		},
	}), nil
}

func uploadZoneFiles() error {
	svc, err := s3Client()
	if err != nil {
		return fmt.Errorf("creating S3 client: %w", err)
	}

	for _, name := range []string{"e2etest.net.toml", "nxdomain.test.toml"} {
		zoneFile, err := os.ReadFile("testdata/" + name)
		if err != nil {
			return fmt.Errorf("reading zone file %s: %w", name, err)
		}

		_, err = svc.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket:      aws.String(bucketName),
			Key:         aws.String(name),
			Body:        bytes.NewReader(zoneFile),
			ContentType: aws.String("application/toml"),
		})
		if err != nil {
			return fmt.Errorf("uploading zone file %s: %w", name, err)
		}

		fmt.Printf("Uploaded %s to S3\n", name)
	}
	return nil
}

func waitForDNS(domain string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	c := dns.Client{Timeout: 2 * time.Second}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn(domain), dns.TypeA)

	for time.Now().Before(deadline) {
		r, _, err := c.Exchange(&m, northstarUDP)
		if err == nil && r.Rcode == dns.RcodeSuccess && len(r.Answer) > 0 {
			fmt.Printf("Northstar is serving %s from S3\n", domain)
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("timeout waiting for DNS zone %s", domain)
}

// --- S3 Zone Loading ---

func TestE2E_S3ZoneLoad(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("e2etest.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, northstarUDP)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "10.20.30.1", a.A.String())
}

// --- UDP Query ---

func TestE2E_UDPQuery(t *testing.T) {
	c := dns.Client{}

	tests := []struct {
		name   string
		domain string
		qtype  uint16
		check  func(t *testing.T, r *dns.Msg)
	}{
		{
			name: "A record", domain: "e2etest.net", qtype: dns.TypeA,
			check: func(t *testing.T, r *dns.Msg) {
				require.NotEmpty(t, r.Answer)
				a, ok := r.Answer[0].(*dns.A)
				require.True(t, ok)
				assert.Equal(t, "10.20.30.1", a.A.String())
			},
		},
		{
			name: "AAAA record", domain: "e2etest.net", qtype: dns.TypeAAAA,
			check: func(t *testing.T, r *dns.Msg) {
				require.NotEmpty(t, r.Answer)
				aaaa, ok := r.Answer[0].(*dns.AAAA)
				require.True(t, ok)
				assert.Equal(t, "2001:db8::1", aaaa.AAAA.String())
			},
		},
		{
			name: "MX record", domain: "e2etest.net", qtype: dns.TypeMX,
			check: func(t *testing.T, r *dns.Msg) {
				require.NotEmpty(t, r.Answer)
				mx, ok := r.Answer[0].(*dns.MX)
				require.True(t, ok)
				assert.Equal(t, "mail.e2etest.net.", mx.Mx)
				assert.Equal(t, uint16(10), mx.Preference)
			},
		},
		{
			name: "TXT record", domain: "e2etest.net", qtype: dns.TypeTXT,
			check: func(t *testing.T, r *dns.Msg) {
				assert.GreaterOrEqual(t, len(r.Answer), 2)
			},
		},
		{
			name: "NS record", domain: "e2etest.net", qtype: dns.TypeNS,
			check: func(t *testing.T, r *dns.Msg) {
				require.GreaterOrEqual(t, len(r.Answer), 2)
				var nsNames []string
				for _, rr := range r.Answer {
					if ns, ok := rr.(*dns.NS); ok {
						nsNames = append(nsNames, ns.Ns)
					}
				}
				assert.Contains(t, nsNames, "ns1.e2etest.net.")
				assert.Contains(t, nsNames, "ns2.e2etest.net.")
			},
		},
		{
			name: "SRV record", domain: "_nats._tcp.e2etest.net", qtype: dns.TypeSRV,
			check: func(t *testing.T, r *dns.Msg) {
				require.NotEmpty(t, r.Answer)
				srv, ok := r.Answer[0].(*dns.SRV)
				require.True(t, ok)
				assert.Equal(t, uint16(4222), srv.Port)
				assert.Equal(t, "node1.e2etest.net.", srv.Target)
			},
		},
		{
			name: "CAA record", domain: "e2etest.net", qtype: dns.TypeCAA,
			check: func(t *testing.T, r *dns.Msg) {
				require.NotEmpty(t, r.Answer)
				caa, ok := r.Answer[0].(*dns.CAA)
				require.True(t, ok)
				assert.Equal(t, "issue", caa.Tag)
				assert.Equal(t, "letsencrypt.org", caa.Value)
			},
		},
		{
			name: "SOA record", domain: "e2etest.net", qtype: dns.TypeSOA,
			check: func(t *testing.T, r *dns.Msg) {
				require.NotEmpty(t, r.Answer)
				soa, ok := r.Answer[0].(*dns.SOA)
				require.True(t, ok)
				assert.Equal(t, "ns1.e2etest.net.", soa.Ns)
				assert.Greater(t, soa.Serial, uint32(0))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := dns.Msg{}
			m.SetQuestion(dns.Fqdn(tt.domain), tt.qtype)
			r, _, err := c.Exchange(&m, northstarUDP)
			require.NoError(t, err)
			assert.Equal(t, dns.RcodeSuccess, r.Rcode)
			tt.check(t, r)
		})
	}
}

// --- TCP Query ---

func TestE2E_TCPQuery(t *testing.T) {
	c := dns.Client{Net: "tcp"}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("e2etest.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, northstarTCP)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "10.20.30.1", a.A.String())
}

// --- DoT Query ---

func TestE2E_DoTQuery(t *testing.T) {
	c := dns.Client{
		Net: "tcp-tls",
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("e2etest.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, northstarDoT)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "10.20.30.1", a.A.String())
}

// --- EDNS0 ---

func TestE2E_EDNS0(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("e2etest.net"), dns.TypeA)
	m.SetEdns0(4096, false)

	r, _, err := c.Exchange(&m, northstarUDP)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)

	opt := r.IsEdns0()
	assert.NotNil(t, opt, "Response should contain EDNS0 OPT record")
	if opt != nil {
		assert.Equal(t, uint16(4096), opt.UDPSize())
	}
}

// --- NXDOMAIN ---

func TestE2E_NXDOMAIN(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("missing.nxdomain.test"), dns.TypeA)

	r, _, err := c.Exchange(&m, northstarUDP)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeNameError, r.Rcode)
	assert.True(t, r.Authoritative)
	require.NotEmpty(t, r.Ns, "response should have an authority section")

	hasSOA := false
	for _, record := range r.Ns {
		if _, ok := record.(*dns.SOA); ok {
			hasSOA = true
			break
		}
	}
	assert.True(t, hasSOA, "authority section should contain an SOA record")
}

// --- Wildcard ---

func TestE2E_Wildcard(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	// random-host.e2etest.net should match *.e2etest.net
	m.SetQuestion(dns.Fqdn("random-host.e2etest.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, northstarUDP)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "10.20.30.99", a.A.String())
}

func TestE2E_WildcardExactOverride(t *testing.T) {
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("api.e2etest.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, northstarUDP)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "10.20.30.50", a.A.String())
}

// --- Zone Reload ---

func TestE2E_ZoneReload(t *testing.T) {
	svc, err := s3Client()
	require.NoError(t, err)

	// Upload an updated zone file with a new IP
	updatedZone := `
version = 1.0

[domain]
domain = "e2etest.net"
created = 2024-01-01T00:00:00Z
modified = 2025-01-01T00:00:00Z
soa = "ns1.e2etest.net."
verified = true
active = true
ownerid = 1

[defaults]
ttl = 300
type = 1
class = 1

[[records]]
domain = ""
address = "10.20.30.200"

[[records]]
domain = "www."
address = "10.20.30.201"
`
	_, err = svc.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String("e2etest.net.toml"),
		Body:   bytes.NewReader([]byte(updatedZone)),
	})
	require.NoError(t, err)

	// Wait for sync (S3_SYNC_RETRY=3 seconds)
	time.Sleep(6 * time.Second)

	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn("e2etest.net"), dns.TypeA)

	r, _, err := c.Exchange(&m, northstarUDP)
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)

	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "10.20.30.200", a.A.String(), "Zone should have been reloaded with new IP")
}

// --- Concurrency ---

func TestE2E_Concurrency(t *testing.T) {
	var wg sync.WaitGroup
	errors := make(chan error, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c := dns.Client{}
			m := dns.Msg{}
			m.SetQuestion(dns.Fqdn("e2etest.net"), dns.TypeA)

			r, _, err := c.Exchange(&m, northstarUDP)
			if err != nil {
				errors <- fmt.Errorf("query %d: %v", idx, err)
				return
			}
			if r.Rcode != dns.RcodeSuccess {
				errors <- fmt.Errorf("query %d: rcode %d", idx, r.Rcode)
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
