package backend

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/mulgadc/northstar/pkg/config"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type Handler struct {
	Conf     *config.Config
	Upstream *Upstream
	inst     *instruments
}

// NewHandler constructs a Handler wired for OTel metrics/traces bound to the
// current global meter/tracer (see pkg/telemetry.Init), and threads the same
// instruments into upstream so forwarder spans/metrics share one tracer.
func NewHandler(conf *config.Config, upstream *Upstream) *Handler {
	inst := newInstruments()
	if upstream != nil {
		upstream.inst = inst
	}
	return &Handler{Conf: conf, Upstream: upstream, inst: inst}
}

// ServeDNS implements dns.Handler for the UDP/TCP/DoT listeners, which pass no
// context, so each query roots a new trace here. DoH instead calls
// ServeDNSContext directly with a context extracted from inbound HTTP headers.
func (h *Handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	h.ServeDNSContext(context.Background(), w, r)
}

// ServeDNSContext handles one query given a caller-supplied context. It starts
// the query span and timer, delegates to serve for response construction,
// writes the reply, then records metrics and span attributes from the final
// response — the single place the outcome of all of ServeDNS's ~10 return
// points is observed.
func (h *Handler) ServeDNSContext(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) {
	start := time.Now()
	ctx, span := h.inst.tracerOrGlobal().Start(ctx, "dns.query")
	defer span.End()

	transport := transportFromWriter(w)

	resp := h.serve(ctx, w, r)
	writeResponse(w, resp)

	elapsed := time.Since(start)
	rcode := resp.Rcode
	outcome := outcomeAttr(rcode)

	countAttrs := []attribute.KeyValue{rcodeAttr(rcode), attribute.String("transport", transport), outcome, attribute.Bool("authoritative", resp.Authoritative)}
	durationAttrs := []attribute.KeyValue{attribute.String("transport", transport), outcome}

	span.SetAttributes(
		attribute.String("network.transport", transport),
		attribute.Int("dns.response.rcode", rcode),
		attribute.Bool("dns.authoritative", resp.Authoritative),
		attribute.Int("dns.answer.count", len(resp.Answer)),
	)
	if len(r.Question) > 0 {
		q := r.Question[0]
		countAttrs = append(countAttrs, qtypeAttr(q.Qtype))
		durationAttrs = append(durationAttrs, qtypeAttr(q.Qtype))
		span.SetAttributes(
			attribute.String("dns.question.name", q.Name),
			attribute.String("dns.question.type", dns.TypeToString[q.Qtype]),
			attribute.String("dns.question.class", dns.ClassToString[q.Qclass]),
		)
	}
	if rcode == dns.RcodeServerFailure {
		span.SetStatus(codes.Error, dns.RcodeToString[rcode])
	}

	h.inst.recordQuery(ctx, countAttrs, durationAttrs, elapsed)
}

// serve builds the response for one query and returns the message to write.
// ServeDNSContext writes the reply and records metrics/span attrs from the
// result, so every return point here returns the final *dns.Msg instead of
// writing directly.
func (h *Handler) serve(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) *dns.Msg {
	msg := dns.Msg{}
	msg.SetReply(r)

	if len(r.Question) == 0 {
		return &msg
	}

	domain := msg.Question[0].Name
	qtype := r.Question[0].Qtype
	qclass := r.Question[0].Qclass

	slog.Debug("DNS request", "domain", domain, "client", w.RemoteAddr().String(), "type", qtype)

	// Handle EDNS0
	var clientBufSize uint16 = 512
	if opt := r.IsEdns0(); opt != nil {
		clientBufSize = max(opt.UDPSize(), 512)
		// Echo EDNS0 OPT in response
		ednsOpt := new(dns.OPT)
		ednsOpt.Hdr.Name = "."
		ednsOpt.Hdr.Rrtype = dns.TypeOPT
		ednsOpt.SetUDPSize(4096)
		msg.Extra = append(msg.Extra, ednsOpt)
	}

	// Check if we are authoritative for this domain
	zone, isAuth := h.Conf.FindZone(domain)

	// Lookup records
	qq := config.DomainLookup{Domain: domain, Type: qtype, Class: qclass}
	h.Conf.Mu.RLock()
	records := make([]config.Records, len(h.Conf.Records[qq]))
	copy(records, h.Conf.Records[qq])
	h.Conf.Mu.RUnlock()

	// If no exact match, try wildcard
	if len(records) == 0 && isAuth {
		wildcardName := wildcardFor(domain, zone)
		if wildcardName != "" {
			wq := config.DomainLookup{Domain: wildcardName, Type: qtype, Class: qclass}
			h.Conf.Mu.RLock()
			records = make([]config.Records, len(h.Conf.Records[wq]))
			copy(records, h.Conf.Records[wq])
			h.Conf.Mu.RUnlock()
		}
	}

	if len(records) == 0 {
		if isAuth {
			// SOA queries always return the generated SOA in answer section
			if qtype == dns.TypeSOA {
				msg.Authoritative = true
				msg.Answer = []dns.RR{h.SOA(domain, zone)}
				return &msg
			}
			// We are authoritative: check if name exists with other types (NODATA vs NXDOMAIN)
			if h.Conf.NameExists(domain) {
				// Name exists but no records for this type → NOERROR (NODATA)
				msg.SetRcode(r, dns.RcodeSuccess)
			} else {
				// Name doesn't exist → NXDOMAIN
				msg.SetRcode(r, dns.RcodeNameError)
			}
			msg.Authoritative = true
			msg.Ns = []dns.RR{h.SOA(domain, zone)}
		} else {
			// Not our zone → recurse to the configured upstream forwarders.
			// With no forwarders configured we refuse (air-gap safe).
			if h.Upstream != nil && h.Upstream.HasServers() {
				if resp, err := h.Upstream.Exchange(ctx, r); err == nil && resp != nil {
					resp.Id = r.Id
					// Over UDP, trim to the client's advertised buffer; if the
					// full answer doesn't fit, Truncate sets TC so the client
					// retries over TCP. Over TCP the full answer is returned.
					if isUDP(w) {
						resp.Truncate(int(clientBufSize))
					}
					return resp
				}
				slog.Debug("recursion failed", "domain", domain)
				msg.SetRcode(r, dns.RcodeServerFailure)
			} else {
				msg.SetRcode(r, dns.RcodeRefused)
			}
		}
		return &msg
	}

	// We have records — build response
	msg.Authoritative = true

	for i := 0; i < len(records); i++ {
		record := &records[i]

		switch qtype {
		case dns.TypeAAAA:
			if record.Type == dns.TypeAAAA {
				msg.Answer = append(msg.Answer, &dns.AAAA{
					Hdr:  dns.RR_Header{Name: domain, Rrtype: dns.TypeAAAA, Class: record.Class, Ttl: record.TTL},
					AAAA: net.ParseIP(record.Address),
				})
			}

		case dns.TypeA:
			if record.Type == dns.TypeA {
				msg.Answer = append(msg.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: domain, Rrtype: dns.TypeA, Class: record.Class, Ttl: record.TTL},
					A:   net.ParseIP(record.Address),
				})
			}

			// CNAME chasing: if an A query finds a CNAME, resolve it
			if record.Type == dns.TypeCNAME {
				msg.Answer = append(msg.Answer, &dns.CNAME{
					Hdr:    dns.RR_Header{Name: domain, Rrtype: dns.TypeCNAME, Class: record.Class, Ttl: record.TTL},
					Target: record.Address,
				})

				lookupRecords, err := h.Upstream.Resolve(ctx, record.Address, dns.TypeA)
				if err == nil {
					for _, rr := range lookupRecords {
						if t, ok := rr.(*dns.A); ok {
							msg.Answer = append(msg.Answer, &dns.A{
								Hdr: dns.RR_Header{Name: t.Hdr.Name, Rrtype: dns.TypeA, Class: t.Hdr.Class, Ttl: t.Hdr.Ttl},
								A:   t.A,
							})
						}
					}
				}
			}

		case dns.TypeCNAME:
			if record.Type == dns.TypeCNAME {
				msg.Answer = append(msg.Answer, &dns.CNAME{
					Hdr:    dns.RR_Header{Name: domain, Rrtype: dns.TypeCNAME, Class: record.Class, Ttl: record.TTL},
					Target: record.Address,
				})
			}

		case dns.TypeSOA:
			msg.Answer = []dns.RR{h.SOA(domain, zone)}

		case dns.TypeTXT:
			if record.Type == dns.TypeTXT {
				msg.Answer = append(msg.Answer, &dns.TXT{
					Hdr: dns.RR_Header{Name: domain, Rrtype: dns.TypeTXT, Class: record.Class, Ttl: record.TTL},
					Txt: []string{record.Address},
				})
			}

		case dns.TypeMX:
			if record.Type == dns.TypeMX {
				msg.Answer = append(msg.Answer, &dns.MX{
					Hdr:        dns.RR_Header{Name: domain, Rrtype: dns.TypeMX, Class: record.Class, Ttl: record.TTL},
					Preference: record.Preference,
					Mx:         record.Address,
				})

				extra := h.lookupExtra(record.Address, dns.TypeA, qclass)
				msg.Extra = append(msg.Extra, extra...)
			}

		case dns.TypeNS:
			if record.Type == dns.TypeNS {
				msg.Answer = append(msg.Answer, &dns.NS{
					Hdr: dns.RR_Header{Name: domain, Rrtype: dns.TypeNS, Class: record.Class, Ttl: record.TTL},
					Ns:  record.Address,
				})

				extra := h.lookupExtra(record.Address, dns.TypeA, qclass)
				msg.Extra = append(msg.Extra, extra...)
			}

		case dns.TypeSRV:
			if record.Type == dns.TypeSRV {
				msg.Answer = append(msg.Answer, &dns.SRV{
					Hdr:      dns.RR_Header{Name: domain, Rrtype: dns.TypeSRV, Class: record.Class, Ttl: record.TTL},
					Priority: record.Priority,
					Weight:   record.Weight,
					Port:     record.Port,
					Target:   record.Address,
				})

				extra := h.lookupExtra(record.Address, dns.TypeA, qclass)
				msg.Extra = append(msg.Extra, extra...)
			}

		case dns.TypeCAA:
			if record.Type == dns.TypeCAA {
				msg.Answer = append(msg.Answer, &dns.CAA{
					Hdr:   dns.RR_Header{Name: domain, Rrtype: dns.TypeCAA, Class: record.Class, Ttl: record.TTL},
					Flag:  record.CAAFlag,
					Tag:   record.CAATag,
					Value: record.Address,
				})
			}

		case dns.TypePTR:
			if record.Type == dns.TypePTR {
				msg.Answer = append(msg.Answer, &dns.PTR{
					Hdr: dns.RR_Header{Name: domain, Rrtype: dns.TypePTR, Class: record.Class, Ttl: record.TTL},
					Ptr: record.Address,
				})
			}

		default:
			msg.SetRcode(r, dns.RcodeRefused)
		}
	}

	// If we built answers, add NS authority section
	if len(msg.Answer) > 0 && isAuth {
		h.addNSAuthority(&msg, zone, qclass)
	}

	// If we somehow ended up with no answers despite having records
	if len(msg.Answer) == 0 && isAuth {
		msg.SetRcode(r, dns.RcodeSuccess)
		msg.Ns = []dns.RR{h.SOA(domain, zone)}
	}

	// Check response size for UDP
	if isUDP(w) {
		maxSize := int(clientBufSize)
		if msg.Len() > maxSize {
			// Set TC bit so client retries over TCP
			msg.Truncated = true
			msg.Answer = nil
			msg.Ns = nil
			// Keep Extra (EDNS0 OPT)
		}
	}

	return &msg
}

func (h *Handler) lookupExtra(address string, qtype uint16, qclass uint16) []dns.RR {
	// Lookup A records for the given address (glue records)
	query := config.DomainLookup{Domain: address, Type: qtype, Class: qclass}
	h.Conf.Mu.RLock()
	records := h.Conf.Records[query]
	h.Conf.Mu.RUnlock()

	var extra []dns.RR
	for _, r := range records {
		if r.Domain == address && r.Type == dns.TypeA {
			extra = append(extra, &dns.A{
				Hdr: dns.RR_Header{Name: r.Domain, Rrtype: dns.TypeA, Class: r.Class, Ttl: r.TTL},
				A:   net.ParseIP(r.Address),
			})
		}
		if r.Domain == address && r.Type == dns.TypeAAAA {
			extra = append(extra, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: r.Domain, Rrtype: dns.TypeAAAA, Class: r.Class, Ttl: r.TTL},
				AAAA: net.ParseIP(r.Address),
			})
		}
	}

	return extra
}

// addNSAuthority adds NS records to the authority section of the response.
func (h *Handler) addNSAuthority(msg *dns.Msg, zone string, qclass uint16) {
	zoneFQDN := zone + "."
	nsLookup := config.DomainLookup{Domain: zoneFQDN, Type: dns.TypeNS, Class: qclass}

	h.Conf.Mu.RLock()
	nsRecords := h.Conf.Records[nsLookup]
	h.Conf.Mu.RUnlock()

	for _, ns := range nsRecords {
		if ns.Type == dns.TypeNS {
			msg.Ns = append(msg.Ns, &dns.NS{
				Hdr: dns.RR_Header{Name: zoneFQDN, Rrtype: dns.TypeNS, Class: ns.Class, Ttl: ns.TTL},
				Ns:  ns.Address,
			})
		}
	}
}

func (h *Handler) lookupSOA(domain string) string {
	h.Conf.Mu.RLock()
	defer h.Conf.Mu.RUnlock()

	soa := h.Conf.Domain[domain].SOA
	if soa == "" {
		soa = fmt.Sprintf("ns.%s.", domain)
	}
	return soa
}

// SOA generates a SOA record. Uses the zone's Modified timestamp for serial
// to ensure resolvers detect zone changes.
func (h *Handler) SOA(domain string, zone string) dns.RR {
	soaName := domain
	if zone != "" {
		soaName = zone + "."
	}

	// Use zone Modified timestamp for serial (monotonically increases on changes)
	var serial uint32
	h.Conf.Mu.RLock()
	if d, ok := h.Conf.Domain[zone]; ok && !d.Modified.IsZero() {
		serial = uint32(d.Modified.Unix()) //nolint:gosec // G115: SOA serial is uint32 per RFC 1035; second-resolution wrap is acceptable
	}
	h.Conf.Mu.RUnlock()

	return &dns.SOA{
		Hdr:     dns.RR_Header{Name: soaName, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60},
		Ns:      h.lookupSOA(zone),
		Mbox:    "hostmaster." + soaName,
		Serial:  serial,
		Refresh: 28800,
		Retry:   7200,
		Expire:  604800,
		Minttl:  60,
	}
}

// wildcardFor returns the wildcard name for a given domain under a zone.
// e.g., "foo.bar.example.com." under zone "example.com" → "*.example.com."
func wildcardFor(name string, zone string) string {
	zoneFQDN := zone + "."
	if !strings.HasSuffix(name, zoneFQDN) {
		return ""
	}
	return "*." + zoneFQDN
}

// isUDP returns true if the response writer is using UDP transport.
func isUDP(w dns.ResponseWriter) bool {
	_, ok := w.RemoteAddr().(*net.UDPAddr)
	return ok
}

// writeResponse writes the DNS reply, logging any write error.
func writeResponse(w dns.ResponseWriter, msg *dns.Msg) {
	if err := w.WriteMsg(msg); err != nil {
		slog.Debug("failed to write DNS response", "error", err)
	}
}
