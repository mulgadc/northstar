package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"

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
	return RenderZone(NewZoneConfig(seed))
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
