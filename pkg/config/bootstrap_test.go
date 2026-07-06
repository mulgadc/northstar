package config

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mutableS3 is a path-style S3 endpoint backed by an in-memory object map that
// supports HEAD, GET and PUT — enough to exercise the bootstrap round-trip.
func mutableS3(t *testing.T, bucket string) (*S3Config, map[string]string) {
	t.Helper()
	var mu sync.Mutex
	objects := map[string]string{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/"+bucket+"/")
		mu.Lock()
		defer mu.Unlock()

		// ListObjectsV2.
		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			var b strings.Builder
			b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
			b.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
			fmt.Fprintf(&b, "<Name>%s</Name>", bucket)
			for k, v := range objects {
				fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>2022-05-27T07:32:00.000Z</LastModified><Size>%d</Size></Contents>", k, len(v))
			}
			b.WriteString(`</ListBucketResult>`)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(b.String()))
			return
		}

		switch r.Method {
		case http.MethodHead:
			if _, ok := objects[key]; ok {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			objects[key] = string(body)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			body, ok := objects[key]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write([]byte(body))
		default:
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)

	return &S3Config{
		Endpoint:  srv.URL,
		Region:    "us-east-1",
		Bucket:    bucket,
		AccessKey: "AKIATEST",
		SecretKey: "secret",
	}, objects
}

func TestRenderBaseZoneParses(t *testing.T) {
	body, err := RenderBaseZone(BaseZoneSeed{
		Domain:      "spx3.net",
		Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.0.0.1"}, {Host: "ns2", IP: "10.0.0.2"}},
		TXT:         []string{"v=spinifex1"},
		TTL:         60,
	})
	require.NoError(t, err)

	// The rendered TOML must parse as a normal zone and expose NS + glue + TXT.
	cfg, err := ReadZoneFromBytes(t, body, "spx3.net")
	require.NoError(t, err)
	assert.Equal(t, "spx3.net", cfg.Domain.Domain)

	var ns, glue, txt int
	for _, rec := range cfg.Records {
		switch rec.Type {
		case 2:
			ns++
		case 1:
			glue++
		case 16:
			txt++
		}
	}
	assert.Equal(t, 2, ns, "two NS records")
	assert.Equal(t, 2, glue, "two glue A records")
	assert.Equal(t, 1, txt, "one TXT record")
}

func TestRenderBaseZoneNoDomain(t *testing.T) {
	_, err := RenderBaseZone(BaseZoneSeed{})
	require.Error(t, err)
}

func TestEnsureBaseZoneCreatesThenIdempotent(t *testing.T) {
	s3cfg, objects := mutableS3(t, "northstar")

	seed := BaseZoneSeed{
		Domain:      "spx3.net",
		Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.11.12.1"}},
		TXT:         []string{"v=spinifex1"},
	}

	// First call creates the zone.
	created, err := EnsureBaseZone(s3cfg, seed)
	require.NoError(t, err)
	assert.True(t, created)
	require.Contains(t, objects, "spx3.net.toml")

	// It is readable back through the normal S3 zone reader.
	full, err := ReadZoneFiles("s3://northstar", s3cfg)
	require.NoError(t, err)
	require.Contains(t, full.Domain, "spx3.net")

	// Second call is a no-op — never overwrites.
	original := objects["spx3.net.toml"]
	created, err = EnsureBaseZone(s3cfg, seed)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, original, objects["spx3.net.toml"])
}

func TestIsNotFound(t *testing.T) {
	assert.True(t, isNotFound(awserr.New(s3.ErrCodeNoSuchKey, "no such key", nil)))
	assert.True(t, isNotFound(awserr.New("NotFound", "not found", nil)))
	assert.True(t, isNotFound(awserr.NewRequestFailure(awserr.New("Whatever", "gone", nil), 404, "req")))
	// A missing bucket is a misprovisioned store, never a missing object —
	// even when the backend wraps it in an HTTP 404.
	assert.False(t, isNotFound(awserr.New(s3.ErrCodeNoSuchBucket, "no such bucket", nil)))
	assert.False(t, isNotFound(awserr.NewRequestFailure(awserr.New(s3.ErrCodeNoSuchBucket, "no such bucket", nil), 404, "req")))
	assert.False(t, isNotFound(awserr.NewRequestFailure(awserr.New("AccessDenied", "denied", nil), 403, "req")))
	assert.False(t, isNotFound(io.EOF))
}

func TestZoneExistsMissing(t *testing.T) {
	s3cfg, _ := mutableS3(t, "northstar")
	exists, err := ZoneExists(s3cfg, "absent.net")
	require.NoError(t, err)
	assert.False(t, exists)
}

// ReadZoneFromBytes writes body to a temp file and parses it, a small bridge for
// asserting on RenderBaseZone output without S3.
func ReadZoneFromBytes(t *testing.T, body []byte, domain string) (ConfigArr, error) {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/" + domain + ".toml"
	require.NoError(t, os.WriteFile(path, body, 0o600))
	return ReadZone(path, time.Now(), nil)
}
