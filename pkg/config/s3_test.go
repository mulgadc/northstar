package config

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeS3 is a minimal path-style S3 endpoint serving ListObjectsV2 and
// GetObject for a fixed set of zone files, enough to exercise the S3 code paths
// in ReadZone/ReadZoneFiles/MonitorConfig without a real backend.
func fakeS3(t *testing.T, bucket string, objects map[string]string) *S3Config {
	t.Helper()
	const lastMod = "2022-05-27T07:32:00.000Z"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ListObjectsV2: GET /bucket/?list-type=2
		if r.URL.Query().Get("list-type") == "2" {
			var b strings.Builder
			b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
			b.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
			fmt.Fprintf(&b, "<Name>%s</Name>", bucket)
			for key, body := range objects {
				fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>%s</LastModified><Size>%d</Size></Contents>", key, lastMod, len(body))
			}
			b.WriteString(`</ListBucketResult>`)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(b.String()))
			return
		}

		// GetObject: GET /bucket/<key>
		prefix := "/" + bucket + "/"
		key := strings.TrimPrefix(r.URL.Path, prefix)
		body, ok := objects[key]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return &S3Config{
		Endpoint:  srv.URL,
		Region:    "us-east-1",
		Bucket:    bucket,
		AccessKey: "AKIATEST",
		SecretKey: "secret",
	}
}

func TestReadZoneS3(t *testing.T) {
	objects := map[string]string{"example.test.toml": zoneBody("example.test", "10.0.0.1")}
	s3cfg := fakeS3(t, "northstar", objects)

	cfg, err := ReadZone("s3://northstar/example.test.toml", time.Now(), s3cfg)
	require.NoError(t, err)
	assert.Equal(t, "example.test", cfg.Domain.Domain)
	require.NotEmpty(t, cfg.Records)
	assert.Equal(t, "10.0.0.1", cfg.Records[0].Address)
}

func TestReadZoneFilesS3(t *testing.T) {
	objects := map[string]string{
		"one.test.toml": zoneBody("one.test", "10.0.0.1"),
		"two.test.toml": zoneBody("two.test", "10.0.0.2"),
		"ignore.txt":    "not a zone",
	}
	s3cfg := fakeS3(t, "northstar", objects)

	cfg := ReadZoneFiles("s3://northstar", s3cfg)
	assert.Len(t, cfg.Domain, 2)
	require.NotEmpty(t, apexRecords(cfg, "one.test"))
	require.NotEmpty(t, apexRecords(cfg, "two.test"))
}

func TestMonitorConfigS3(t *testing.T) {
	objects := map[string]string{"sync.test.toml": zoneBody("sync.test", "10.0.0.7")}
	s3cfg := fakeS3(t, "northstar", objects)

	cfg := &Config{Domain: map[string]Domain{}, Records: map[DomainLookup][]Records{}}
	go cfg.MonitorConfig(t.Context(), "s3://northstar", s3cfg, 50*time.Millisecond)

	require.Eventually(t, func() bool {
		return len(apexRecords(cfg, "sync.test")) > 0
	}, 3*time.Second, 50*time.Millisecond)
}
