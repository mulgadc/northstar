package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
)

// NameserverSeed is one authoritative nameserver for a generated base zone: an
// NS label relative to the zone (e.g. "ns1") plus its glue A address.
type NameserverSeed struct {
	Host string
	IP   string
}

// BaseZoneSeed describes the records written when seeding a base zone: the apex
// SOA/NS set plus a small TXT marker. The control plane fills this from cluster
// topology before calling EnsureBaseZone.
type BaseZoneSeed struct {
	Domain      string
	Nameservers []NameserverSeed
	TXT         []string
	TTL         uint32
}

const defaultBaseZoneTTL = 300

// RenderBaseZone produces a TOML zone body for the base domain in the same shape
// as on-disk/S3 zone files (apex NS records with glue A records and TXT).
func RenderBaseZone(seed BaseZoneSeed) ([]byte, error) {
	if seed.Domain == "" {
		return nil, errors.New("base zone domain is required")
	}

	ttl := seed.TTL
	if ttl == 0 {
		ttl = defaultBaseZoneTTL
	}

	var b strings.Builder
	b.WriteString("version = 1.0\n")
	b.WriteString("[domain]\n")
	fmt.Fprintf(&b, "domain = %q\n", seed.Domain)
	fmt.Fprintf(&b, "modified = %s\n", time.Now().UTC().Format(time.RFC3339))
	b.WriteString("active = true\n")
	// Point the SOA MNAME at the first nameserver (a real, glued host) rather
	// than the backend's synthesized ns.<domain> default.
	if len(seed.Nameservers) > 0 {
		fmt.Fprintf(&b, "soa = \"%s.%s.\"\n", seed.Nameservers[0].Host, seed.Domain)
	}
	b.WriteString("[defaults]\n")
	fmt.Fprintf(&b, "ttl = %d\n", ttl)
	b.WriteString("type = 1\n")
	b.WriteString("class = 1\n")

	for _, ns := range seed.Nameservers {
		// Apex NS record pointing at the nameserver hostname.
		b.WriteString("[[records]]\n")
		b.WriteString("domain = \"\"\n")
		b.WriteString("type = 2\n")
		fmt.Fprintf(&b, "address = \"%s.%s.\"\n", ns.Host, seed.Domain)

		// Glue A record so resolvers can reach the nameserver.
		if ns.IP != "" {
			b.WriteString("[[records]]\n")
			fmt.Fprintf(&b, "domain = %q\n", ns.Host+".")
			b.WriteString("type = 1\n")
			fmt.Fprintf(&b, "address = %q\n", ns.IP)
		}
	}

	for _, txt := range seed.TXT {
		b.WriteString("[[records]]\n")
		b.WriteString("domain = \"\"\n")
		b.WriteString("type = 16\n")
		fmt.Fprintf(&b, "address = %q\n", txt)
	}

	return []byte(b.String()), nil
}

// ZoneExists reports whether <domain>.toml is present in the S3 bucket.
func ZoneExists(s3cfg *S3Config, domain string) (bool, error) {
	if s3cfg == nil || s3cfg.Bucket == "" {
		return false, errors.New("s3 config with bucket required")
	}

	svc := s3.New(newS3Session(s3cfg))
	_, err := svc.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(s3cfg.Bucket),
		Key:    aws.String(domain + ".toml"),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// WriteZoneFile uploads body to the bucket as <domain>.toml. Requires write
// (PutObject) credentials — used by the control plane, not the read-only daemon.
func WriteZoneFile(s3cfg *S3Config, domain string, body []byte) error {
	if s3cfg == nil || s3cfg.Bucket == "" {
		return errors.New("s3 config with bucket required")
	}

	svc := s3.New(newS3Session(s3cfg))
	_, err := svc.PutObject(&s3.PutObjectInput{
		Bucket:      aws.String(s3cfg.Bucket),
		Key:         aws.String(domain + ".toml"),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/toml"),
	})
	return err
}

// EnsureBaseZone creates <domain>.toml from seed when it is not already present
// in the bucket. It never overwrites an existing zone (record management is the
// control plane's job). Returns true when it created the zone.
func EnsureBaseZone(s3cfg *S3Config, seed BaseZoneSeed) (bool, error) {
	exists, err := ZoneExists(s3cfg, seed.Domain)
	if err != nil {
		return false, fmt.Errorf("check base zone %s: %w", seed.Domain, err)
	}
	if exists {
		slog.Debug("base zone already present", "domain", seed.Domain)
		return false, nil
	}

	body, err := RenderBaseZone(seed)
	if err != nil {
		return false, err
	}
	if err := WriteZoneFile(s3cfg, seed.Domain, body); err != nil {
		return false, fmt.Errorf("write base zone %s: %w", seed.Domain, err)
	}

	slog.Info("created base zone", "domain", seed.Domain, "nameservers", len(seed.Nameservers))
	return true, nil
}

// isNotFound reports whether an S3 error is a 404 (missing key/bucket), tolerant
// of the various codes S3-compatible backends return for HeadObject.
func isNotFound(err error) bool {
	var rf awserr.RequestFailure
	if errors.As(err, &rf) && rf.StatusCode() == 404 {
		return true
	}
	var aerr awserr.Error
	if errors.As(err, &aerr) {
		switch aerr.Code() {
		case s3.ErrCodeNoSuchKey, "NotFound":
			return true
		}
	}
	return false
}
