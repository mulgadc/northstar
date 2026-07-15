package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/pelletier/go-toml/v2"
)

// Record type/class constants used by the editors.
const (
	TypeA   uint16 = 1
	TypeNS  uint16 = 2
	TypeTXT uint16 = 16
	ClassIN uint16 = 1
)

// NewZoneConfig builds an in-memory zone (relative-label records, before
// ApplyDefaults) from a seed: apex SOA/NS with glue A records plus TXT markers.
// RenderBaseZone and the on-demand zone creation path both use it.
func NewZoneConfig(seed BaseZoneSeed) ConfigArr {
	ttl := seed.TTL
	if ttl == 0 {
		ttl = defaultBaseZoneTTL
	}

	cfg := ConfigArr{
		Version: 1.0,
		Domain: Domain{
			Domain:   seed.Domain,
			Modified: time.Now().UTC(),
			Active:   true,
		},
		Defaults: Defaults{TTL: ttl, Type: TypeA, Class: ClassIN},
	}
	// SOA MNAME points at the first nameserver (a real, glued host).
	if len(seed.Nameservers) > 0 {
		cfg.Domain.SOA = fmt.Sprintf("%s.%s.", seed.Nameservers[0].Host, seed.Domain)
	}

	for _, ns := range seed.Nameservers {
		cfg.Records = append(cfg.Records, Records{
			Domain:  "",
			Type:    TypeNS,
			Address: fmt.Sprintf("%s.%s.", ns.Host, seed.Domain),
		})
		if ns.IP != "" {
			cfg.Records = append(cfg.Records, Records{
				Domain:  ns.Host + ".",
				Type:    TypeA,
				Address: ns.IP,
			})
		}
	}
	for _, txt := range seed.TXT {
		cfg.Records = append(cfg.Records, Records{Domain: "", Type: TypeTXT, Address: txt})
	}
	return cfg
}

// RenderZone serialises a zone (relative-label records) back to the on-disk/S3
// TOML format read by ReadZone. It is the inverse of an un-defaulted parse:
// records must hold labels relative to the zone, not FQDNs.
func RenderZone(cfg ConfigArr) ([]byte, error) {
	if cfg.Domain.Domain == "" {
		return nil, errors.New("zone domain is required")
	}

	ttl := cfg.Defaults.TTL
	if ttl == 0 {
		ttl = defaultBaseZoneTTL
	}
	rtype := cfg.Defaults.Type
	if rtype == 0 {
		rtype = TypeA
	}
	class := cfg.Defaults.Class
	if class == 0 {
		class = ClassIN
	}

	var b strings.Builder
	b.WriteString("version = 1.0\n")
	b.WriteString("[domain]\n")
	fmt.Fprintf(&b, "domain = %q\n", cfg.Domain.Domain)
	modified := cfg.Domain.Modified
	if modified.IsZero() {
		modified = time.Now().UTC()
	}
	fmt.Fprintf(&b, "modified = %s\n", modified.UTC().Format(time.RFC3339))
	b.WriteString("active = true\n")
	if cfg.Domain.SOA != "" {
		fmt.Fprintf(&b, "soa = %q\n", cfg.Domain.SOA)
	}
	b.WriteString("[defaults]\n")
	fmt.Fprintf(&b, "ttl = %d\n", ttl)
	fmt.Fprintf(&b, "type = %d\n", rtype)
	fmt.Fprintf(&b, "class = %d\n", class)

	for _, rec := range cfg.Records {
		b.WriteString("[[records]]\n")
		fmt.Fprintf(&b, "domain = %q\n", rec.Domain)
		fmt.Fprintf(&b, "type = %d\n", rec.Type)
		if rec.Class != 0 {
			fmt.Fprintf(&b, "class = %d\n", rec.Class)
		}
		if rec.TTL != 0 {
			fmt.Fprintf(&b, "ttl = %d\n", rec.TTL)
		}
		if rec.Root != "" {
			fmt.Fprintf(&b, "root = %q\n", rec.Root)
		}
		if rec.Address != "" {
			fmt.Fprintf(&b, "address = %q\n", rec.Address)
		}
		switch rec.Type {
		case 15: // MX
			fmt.Fprintf(&b, "preference = %d\n", rec.Preference)
		case 33: // SRV
			fmt.Fprintf(&b, "priority = %d\n", rec.Priority)
			fmt.Fprintf(&b, "weight = %d\n", rec.Weight)
			fmt.Fprintf(&b, "port = %d\n", rec.Port)
		case 257: // CAA
			fmt.Fprintf(&b, "caa_flag = %d\n", rec.CAAFlag)
			fmt.Fprintf(&b, "caa_tag = %q\n", rec.CAATag)
		}
	}

	return []byte(b.String()), nil
}

// ReadZoneRaw downloads <domain>.toml and parses it WITHOUT applying defaults,
// so record domains stay relative to the zone (suitable for read-modify-write).
// The bool reports whether the zone object exists.
func ReadZoneRaw(s3cfg *S3Config, domain string) (ConfigArr, bool, error) {
	var cfg ConfigArr
	if s3cfg == nil || s3cfg.Bucket == "" {
		return cfg, false, errors.New("s3 config with bucket required")
	}

	svc := newS3Client(s3cfg)
	out, err := svc.GetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: aws.String(s3cfg.Bucket),
		Key:    aws.String(domain + ".toml"),
	})
	if err != nil {
		if isNotFound(err) {
			return cfg, false, nil
		}
		return cfg, false, err
	}
	defer func() { _ = out.Body.Close() }()

	if err := toml.NewDecoder(out.Body).Decode(&cfg); err != nil {
		return cfg, false, fmt.Errorf("parse zone %s: %w", domain, err)
	}
	return cfg, true, nil
}

// UpsertRecord replaces the RRset for (label, rtype) with a single record (AWS
// UPSERT semantics for system records), or appends it when absent. label is
// relative to the zone ("" = apex). Returns true if anything changed.
func (cfg *ConfigArr) UpsertRecord(label string, rtype, class uint16, address string, ttl uint32) bool {
	if class == 0 {
		class = ClassIN
	}
	rec := Records{Domain: label, Type: rtype, Class: class, Address: address, TTL: ttl}

	var kept []Records
	var replaced, identical bool
	for _, existing := range cfg.Records {
		if strings.EqualFold(existing.Domain, label) && existing.Type == rtype {
			if !replaced {
				if existing.Address == address && existing.TTL == ttl {
					identical = true
				}
				kept = append(kept, rec)
				replaced = true
			}
			continue
		}
		kept = append(kept, existing)
	}
	if !replaced {
		kept = append(kept, rec)
	}
	cfg.Records = kept
	return !identical || !replaced
}

// RemoveRecord drops every record matching (label, rtype). When value is
// non-empty only records with that address are removed. Returns true if any
// record was removed.
func (cfg *ConfigArr) RemoveRecord(label string, rtype uint16, value string) bool {
	var kept []Records
	var removed bool
	for _, existing := range cfg.Records {
		match := strings.EqualFold(existing.Domain, label) && existing.Type == rtype &&
			(value == "" || existing.Address == value)
		if match {
			removed = true
			continue
		}
		kept = append(kept, existing)
	}
	cfg.Records = kept
	return removed
}
