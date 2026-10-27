package config

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

// ErrZoneCorrupt marks a zone object that was fetched successfully but does not
// parse. Callers need it to tell "the stored bytes are bad, rebuilding is safe"
// apart from "the backend is unreachable, leave the zone alone".
var ErrZoneCorrupt = errors.New("zone object is corrupt")

// ErrZoneNotFound marks a zone whose object does not exist, as distinct from a
// backend that could not be asked.
var ErrZoneNotFound = errors.New("zone object not found")

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
	fmt.Fprintf(&b, "domain = %s\n", tomlString(cfg.Domain.Domain))
	modified := cfg.Domain.Modified
	if modified.IsZero() {
		modified = time.Now().UTC()
	}
	fmt.Fprintf(&b, "modified = %s\n", modified.UTC().Format(time.RFC3339))
	b.WriteString("active = true\n")
	if cfg.Domain.SOA != "" {
		fmt.Fprintf(&b, "soa = %s\n", tomlString(cfg.Domain.SOA))
	}
	if cfg.Domain.SOARecord != "" {
		fmt.Fprintf(&b, "soa_record = %s\n", tomlString(cfg.Domain.SOARecord))
		fmt.Fprintf(&b, "soa_ttl = %d\n", cfg.Domain.SOATTL)
	}
	b.WriteString("[defaults]\n")
	fmt.Fprintf(&b, "ttl = %d\n", ttl)
	fmt.Fprintf(&b, "type = %d\n", rtype)
	fmt.Fprintf(&b, "class = %d\n", class)

	for _, rec := range cfg.Records {
		b.WriteString("[[records]]\n")
		fmt.Fprintf(&b, "domain = %s\n", tomlString(rec.Domain))
		fmt.Fprintf(&b, "type = %d\n", rec.Type)
		if rec.Class != 0 {
			fmt.Fprintf(&b, "class = %d\n", rec.Class)
		}
		if rec.TTL != 0 {
			fmt.Fprintf(&b, "ttl = %d\n", rec.TTL)
		}
		if rec.Root != "" {
			fmt.Fprintf(&b, "root = %s\n", tomlString(rec.Root))
		}
		if rec.Address != "" {
			fmt.Fprintf(&b, "address = %s\n", tomlString(rec.Address))
		}
		if len(rec.Strings) > 0 {
			quoted := make([]string, len(rec.Strings))
			for i, str := range rec.Strings {
				quoted[i] = tomlString(str)
			}
			fmt.Fprintf(&b, "strings = [%s]\n", strings.Join(quoted, ", "))
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
			fmt.Fprintf(&b, "caa_tag = %s\n", tomlString(rec.CAATag))
		}
	}

	return []byte(b.String()), nil
}

// tomlString quotes s as a TOML basic string. Go's %q is not a substitute: it
// writes \x escapes, which TOML rejects, so a control byte in a record value
// would make the whole zone unparseable.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
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
		Key:    aws.String(zoneObjectKey(domain)),
	})
	if err != nil {
		if isNotFound(err) {
			return cfg, false, nil
		}
		return cfg, false, err
	}
	defer func() { _ = out.Body.Close() }()

	if err := toml.NewDecoder(out.Body).Decode(&cfg); err != nil {
		// Wrap both so callers can branch on ErrZoneCorrupt while the decoder's
		// own message survives for logs.
		return cfg, false, fmt.Errorf("parse zone %s: %w: %w", domain, ErrZoneCorrupt, err)
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

// SetRecordSet replaces the RRset for (label, rtype) with exactly the given
// addresses, unlike UpsertRecord which is fixed to a single record. It exists
// for record classes whose desired answer is more than one address at once
// (e.g. a name that must resolve to every cluster node's own gateway, rather
// than to one resource that answers the same from anywhere). label is
// relative to the zone (""= apex). Returns true if the resulting set differs
// from what was there before.
func (cfg *ConfigArr) SetRecordSet(label string, rtype, class uint16, addresses []string, ttl uint32) bool {
	if class == 0 {
		class = ClassIN
	}
	var old []Records
	var rest []Records
	for _, existing := range cfg.Records {
		if strings.EqualFold(existing.Domain, label) && existing.Type == rtype {
			old = append(old, existing)
			continue
		}
		rest = append(rest, existing)
	}

	want := make([]Records, 0, len(addresses))
	for _, addr := range addresses {
		want = append(want, Records{Domain: label, Type: rtype, Class: class, Address: addr, TTL: ttl})
	}

	cfg.Records = append(rest, want...)
	return !recordSetsEqual(old, want)
}

// recordSetsEqual compares two record sets for the same (label, type) as
// multisets of (address, ttl), ignoring order: SetRecordSet rebuilds its
// desired set deterministically each cycle, but existing on-disk order is not
// guaranteed to match.
func recordSetsEqual(a, b []Records) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(r Records) string { return r.Address + "\x00" + strconv.FormatUint(uint64(r.TTL), 10) }
	counts := map[string]int{}
	for _, r := range a {
		counts[key(r)]++
	}
	for _, r := range b {
		counts[key(r)]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}
