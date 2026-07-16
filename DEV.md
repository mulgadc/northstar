# Northstar v2 - Development Plan

## Implementation Status

### Completed (v2 branch)

- [x] **Go modernization** — Go 1.24, updated deps (miekg/dns v1.1.72), replaced deprecated ioutil
- [x] **TCP listener** — Both UDP and TCP on same port (RFC 5966 compliance)
- [x] **EDNS0 support** — OPT record handling, client buffer size awareness (RFC 6891)
- [x] **Fixed response codes** — NXDOMAIN for missing names, NODATA for missing types, REFUSED for unknown zones
- [x] **NS authority section** — NS records in authority section of all authoritative responses
- [x] **Fixed SOA serial** — Uses zone Modified timestamp instead of time.Now()
- [x] **Fixed RWMutex** — Single lock per query, copy records slice before iterating
- [x] **Graceful shutdown** — SIGTERM/SIGINT signal handling
- [x] **DNS-over-TLS (DoT)** — TLS listener on configurable port (default 853)
- [x] **SRV records** — Service discovery for Mulga Spinifex (RFC 2782)
- [x] **CAA records** — Certificate authority authorization (RFC 8659)
- [x] **PTR records** — Reverse DNS (RFC 1035)
- [x] **Wildcard records** — `*.domain.com` catch-all with exact match priority
- [x] **Configurable upstream** — TLS/plaintext upstream resolvers with failover (`NORTHSTAR_UPSTREAM`)
- [x] **S3 custom endpoint** — `NORTHSTAR_S3_ENDPOINT` for Predastore/S3-compatible backends
- [x] **S3 TLS verification** — Verified by default with an explicit standalone environment opt-out
- [x] **Comprehensive unit tests** — 21 tests covering all record types, TCP/UDP, EDNS0, response codes, wildcards, concurrency
- [x] **E2E test infrastructure** — Docker Compose with Predastore (S3 backend) + Northstar, 12 E2E test scenarios
- [x] **Race condition clean** — All tests pass under `go test -race`

### Future Roadmap

- [ ] DNS-over-HTTPS (DoH) — RFC 8484
- [ ] DNSSEC signing
- [ ] Prometheus metrics
- [ ] Rate limiting / DDoS protection
- [ ] Dynamic record API (HTTP)
- [ ] Split-horizon DNS (views)
- [ ] Health-aware DNS
- [ ] Response caching
- [ ] migrate to github.com/miekg/dns/v2
- [ ] make e2e part of main module, match spinifex

---

## Current State Assessment

Northstar is a lightweight authoritative DNS server (v1.0.1) that has been running in production for years. It uses an in-memory hashmap for O(1) lookups (~160us per query), loads zone files from S3 or local filesystem in TOML format, and supports A, AAAA, CNAME, MX, NS, TXT, and SOA record types.

### Why Public Resolvers Fail to Resolve Hosted Domains

Domains like `spx3.net`, `neon.us-west-1.spx3.net`, and `helium.lan.us-west-1.spx3.net` fail to resolve via `8.8.8.8` / `1.1.1.1` due to a combination of protocol compliance gaps:

1. **No TCP support (RFC 5966 violation)** - Public resolvers *require* TCP fallback. When a response is truncated or the resolver wants to validate, it retries over TCP. Northstar drops these queries silently since it only binds UDP. Google/Cloudflare resolvers mark the server as unreliable after TCP failures.

2. **No EDNS0 support (RFC 6891)** - Modern resolvers send EDNS0 OPT records in every query to advertise larger buffer sizes and signal capabilities. Northstar ignores these and the lack of EDNS0 response causes resolvers to fall back to 512-byte UDP mode or mark the server as non-compliant.

3. **Broken NXDOMAIN/NODATA semantics** - The server returns `REFUSED` (rcode 5) for domains it doesn't have records for (`backend.go:57`), when it should return `NXDOMAIN` (rcode 3) for non-existent names under authoritative zones, and `NOERROR` with empty answer for existing names with no matching type. Public resolvers interpret `REFUSED` as "this server doesn't serve this zone" and try other nameservers or give up.

4. **SOA serial is time-based and non-monotonic** - `Serial: uint32(time.Now().Truncate(time.Hour).Unix())` (`backend.go:318`) means the serial changes every hour but doesn't reflect actual zone changes. Resolvers use SOA serial for cache invalidation and zone transfer decisions. A serial that doesn't increment on changes means resolvers may cache stale NXDOMAIN responses indefinitely.

5. **Missing NS authority section** - When responding to queries, the server never includes NS records in the Authority section. RFC 1034 requires authoritative servers to include NS records pointing to themselves in authority responses, which resolvers use to validate delegation.

6. **Hardcoded unencrypted upstream** - CNAME chasing uses `1.1.1.1:53` over plaintext UDP (`backend.go:297`), a single point of failure with no fallback.

---

## Phase 1: Protocol Compliance (Reliability Fix)

**Goal**: Make northstar a standards-compliant authoritative DNS server that public resolvers trust.

### 1.1 Add TCP Listener

Bind both UDP and TCP on the same port. The miekg/dns library already supports this - just add a second `dns.Server` with `Net: "tcp"`.

```go
// Start both listeners concurrently
srvUDP := &dns.Server{Addr: addr, Net: "udp"}
srvTCP := &dns.Server{Addr: addr, Net: "tcp"}
go srvTCP.ListenAndServe()
srvUDP.ListenAndServe()
```

**Files**: `pkg/backend/backend.go` (StartDaemon)

### 1.2 EDNS0 Support

Handle OPT records in queries. When a client sends EDNS0, echo back an OPT record with the server's buffer size. This tells resolvers we support modern DNS.

- Parse incoming OPT record for client's advertised buffer size
- Include OPT record in response with server's buffer size (4096 bytes, standard)
- Use the client's buffer size as the max response size instead of hardcoded 9192
- Set the `TC` (truncated) flag properly when response exceeds buffer size, so client retries over TCP

**Files**: `pkg/backend/backend.go` (ServeDNS)

### 1.3 Fix Response Codes

The current response code logic is incorrect:

| Scenario | Current | Correct |
|----------|---------|---------|
| Domain exists, record type exists | NOERROR | NOERROR (correct) |
| Domain exists, record type missing | REFUSED | **NOERROR** (empty answer + SOA in authority) |
| Domain doesn't exist under our zone | REFUSED | **NXDOMAIN** + SOA in authority |
| Not our zone at all | (n/a) | REFUSED |

Implementation:
- Track which zones we are authoritative for (already have `Config.Domain` map)
- On query: check if the queried name falls under any of our zones
- If under our zone but name doesn't exist: NXDOMAIN
- If name exists but no matching type: NOERROR with empty answer
- If not under any of our zones: REFUSED
- Always include SOA in the authority section for NXDOMAIN/NOERROR-empty responses

**Files**: `pkg/backend/backend.go` (ServeDNS), `pkg/config/config.go` (add zone membership check)

### 1.4 NS Authority Records

Add NS records to the authority section of every authoritative response. This requires zone files to define NS records for the zone apex, which should already exist in production configs.

For every response where we are authoritative:
```
;; AUTHORITY SECTION:
spx3.net.  3600  IN  NS  ns1.spx3.net.
spx3.net.  3600  IN  NS  ns2.spx3.net.
```

**Files**: `pkg/backend/backend.go` (ServeDNS - populate `msg.Ns`)

### 1.5 Fix SOA Serial

Replace time-based serial with a monotonically increasing serial derived from zone modification timestamps:

```go
Serial: uint32(domain.Modified.Unix()) // Increments on every zone change
```

This ensures resolvers detect zone updates and refresh their caches.

**Files**: `pkg/backend/backend.go` (SOA method), `pkg/config/config.go` (track modification timestamps properly)

### 1.6 Fix RWMutex Usage

Current code locks/unlocks per record inside the loop (`backend.go:66-68`), which doesn't actually protect the slice from concurrent modification. Should be:

```go
this.Conf.Mu.RLock()
records := this.Conf.Records[qq]
this.Conf.Mu.RUnlock()

for i := 0; i < len(records); i++ {
    // process records...
}
```

**Files**: `pkg/backend/backend.go` (ServeDNS)

### 1.7 Graceful Shutdown

Add signal handling (SIGTERM, SIGINT) to cleanly shut down both UDP and TCP listeners:

```go
sig := make(chan os.Signal, 1)
signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
<-sig
srvUDP.Shutdown()
srvTCP.Shutdown()
```

**Files**: `pkg/backend/backend.go` (StartDaemon)

---

## Phase 2: Encrypted DNS

**Goal**: Support DNS-over-TLS (DoT) and DNS-over-HTTPS (DoH) for encrypted query transport.

### 2.1 DNS-over-TLS (DoT) - RFC 7858

Standard encrypted DNS on port 853. miekg/dns supports TLS natively:

```go
srvDoT := &dns.Server{
    Addr:      addr + ":853",
    Net:       "tcp-tls",
    TLSConfig: tlsConfig,
}
```

Configuration:
```
NORTHSTAR_TLS_CERT="/path/to/cert.pem"
NORTHSTAR_TLS_KEY="/path/to/key.pem"
DOT_PORT=853
```

The TLS certificate must match the server's hostname (e.g., `ns1.spx3.net`). Support automatic certificate loading and reload on file change for rotation.

**Files**: `pkg/backend/backend.go` (new DoT server), `cmd/northstar/main.go` (TLS env vars)

### 2.2 DNS-over-HTTPS (DoH) - RFC 8484

HTTP/2 endpoint for DNS queries. This is what browsers and modern clients use. Implement using Go's `net/http` with TLS:

- Endpoint: `GET/POST /dns-query`
- Accept: `application/dns-message` (wire format) and `application/dns-json` (JSON format)
- Uses the same handler logic as UDP/TCP but wraps the DNS message in HTTP

```go
http.HandleFunc("/dns-query", func(w http.ResponseWriter, r *http.Request) {
    // Extract DNS message from HTTP request body or ?dns= query param
    // Process through existing ServeDNS logic
    // Return wire-format DNS response
})
```

Configuration:
```
DOH_PORT=443
```

**Files**: New `pkg/backend/doh.go`, reuse existing ServeDNS handler

### 2.3 Encrypted Upstream Resolver

Replace hardcoded `1.1.1.1:53` plaintext for CNAME chasing with encrypted upstream:

- Use DoT (`1.1.1.1:853`) or DoH for upstream queries
- Support configurable upstream resolvers with failover:
  ```
  NORTHSTAR_UPSTREAM="tls://1.1.1.1:853,tls://8.8.8.8:853"
  ```
- Connection pooling for upstream TLS connections

**Files**: `pkg/backend/backend.go` (lookupHost), new `pkg/backend/upstream.go`

---

## Phase 3: Missing Record Types & Features

**Goal**: Support all record types needed for production DNS and Mulga Spinifex service discovery.

### 3.1 SRV Records (RFC 2782)

Critical for Mulga Spinifex service discovery. SRV records map service names to host:port pairs:

```
_nats._tcp.spinifex.spx3.net.  300  IN  SRV  10 0 4222 node1.spinifex.spx3.net.
_awsgw._tcp.spinifex.spx3.net. 300  IN  SRV  10 0 9999 node1.spinifex.spx3.net.
```

Zone file format:
```toml
[[records]]
domain = "_nats._tcp."
type = 33          # SRV
priority = 10
weight = 0
port = 4222
address = "node1.spinifex.spx3.net."
```

Add `Priority`, `Weight`, and `Port` fields to the `Records` struct.

**Files**: `pkg/config/config.go` (Records struct), `pkg/backend/backend.go` (SRV case in ServeDNS)

### 3.2 CAA Records (RFC 8659)

Certificate Authority Authorization - controls which CAs can issue certificates for a domain. Required for proper TLS/HTTPS:

```toml
[[records]]
domain = ""
type = 257         # CAA
address = "letsencrypt.org"
caa_flag = 0
caa_tag = "issue"
```

**Files**: `pkg/config/config.go`, `pkg/backend/backend.go`

### 3.3 PTR Records (RFC 1035)

Reverse DNS lookups. Needed for mail server reputation and network diagnostics:

```toml
[[records]]
domain = "1.1.100.203.in-addr.arpa."
type = 12          # PTR
address = "host-1.spx3.net."
```

**Files**: `pkg/config/config.go`, `pkg/backend/backend.go`

### 3.4 Wildcard Record Support

Support wildcard entries (`*.example.com`) for catch-all subdomains. Essential for Mulga Spinifex where dynamic subdomains map to compute instances:

```toml
[[records]]
domain = "*."
address = "203.100.1.1"
```

Lookup logic: if exact match fails, strip the leftmost label and try `*.<remaining>`.

**Files**: `pkg/backend/backend.go` (ServeDNS lookup fallback)

---

## Phase 4: Mulga Spinifex Integration

**Goal**: Make northstar the internal and public-facing DNS server for Mulga Spinifex infrastructure.

### 4.1 Dynamic Record API

HTTP API for Mulga Spinifex services to register/deregister DNS records at runtime without modifying zone files:

```
POST   /api/v1/records    - Create record
DELETE /api/v1/records     - Delete record
GET    /api/v1/records     - List records for a zone
PUT    /api/v1/records     - Update record
GET    /api/v1/zones       - List all zones
GET    /api/v1/health      - Health check
```

Records added via API are stored in-memory and optionally persisted to S3. This enables Mulga Spinifex's formation server to register node DNS entries during `spinifex admin join`.

Authentication via shared secret or mTLS:
```
NORTHSTAR_API_PORT=8053
NORTHSTAR_API_KEY="shared-secret"
```

**Files**: New `pkg/api/api.go`, `pkg/api/handlers.go`

### 4.2 Split-Horizon DNS (Views)

Serve different responses based on the client's source network. Mulga Spinifex needs internal IPs for cluster traffic and public IPs for external access:

```
Internal query (10.0.0.0/8):
  node1.spinifex.spx3.net -> 10.0.1.10

External query:
  node1.spinifex.spx3.net -> 203.100.1.50
```

Zone file extension:
```toml
[[records]]
domain = "node1.spinifex."
address = "10.0.1.10"
view = "internal"       # Only served to internal networks

[[records]]
domain = "node1.spinifex."
address = "203.100.1.50"
view = "external"       # Only served to external networks
```

Configuration:
```
NORTHSTAR_INTERNAL_NETS="10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
```

**Files**: `pkg/config/config.go` (Records struct, view field), `pkg/backend/backend.go` (client IP filtering)

### 4.3 Health-Aware DNS

Remove records from responses when the target host fails health checks. Prevents routing traffic to dead Mulga Spinifex nodes:

```toml
[[records]]
domain = "api.spinifex."
address = "10.0.1.10"
healthcheck = "tcp://10.0.1.10:9999"  # Check if AWS gateway is up
```

Health checker runs as a background goroutine, marks records as unhealthy, and excludes them from responses. Falls back to returning all records if all are unhealthy (better than nothing).

**Files**: New `pkg/health/health.go`, integration in `pkg/backend/backend.go`

### 4.4 Mulga Spinifex Auto-Registration

When deployed as part of Mulga Spinifex, automatically generate DNS records for:

- `node-{id}.{region}.spinifex.{domain}` - Individual node addresses
- `_nats._tcp.spinifex.{domain}` - SRV records for NATS cluster discovery
- `_awsgw._tcp.spinifex.{domain}` - SRV records for API gateway
- `_predastore._tcp.spinifex.{domain}` - SRV records for S3-compatible storage
- `api.spinifex.{domain}` - Round-robin A records for all healthy API gateways

This can be driven by the dynamic record API (4.1) called from Mulga Spinifex's formation server, or by watching a NATS topic for node join/leave events.

**Files**: New `pkg/spinifex/spinifex.go` (optional, or driven entirely by API)

---

## Phase 5: Operational Hardening

**Goal**: Production reliability, observability, and security.

### 5.1 Prometheus Metrics

Export metrics for monitoring:

```
northstar_queries_total{type="A",rcode="NOERROR"}
northstar_query_duration_seconds{type="A"}
northstar_zones_loaded
northstar_records_loaded
northstar_upstream_queries_total
northstar_upstream_errors_total
northstar_s3_sync_duration_seconds
northstar_s3_sync_errors_total
```

Expose on configurable HTTP port:
```
NORTHSTAR_METRICS_PORT=9153
```

**Files**: New `pkg/metrics/metrics.go`, instrumentation in `pkg/backend/backend.go`

### 5.2 Rate Limiting

Prevent DNS amplification attacks. Implement per-source-IP rate limiting:

- Token bucket per source IP (default: 100 queries/sec)
- Response Rate Limiting (RRL) for identical responses
- Configurable via env vars:
  ```
  NORTHSTAR_RATE_LIMIT=100        # queries/sec per IP
  NORTHSTAR_RATE_LIMIT_BURST=200  # burst allowance
  ```

**Files**: New `pkg/ratelimit/ratelimit.go`, integration in `pkg/backend/backend.go`

### 5.3 DNSSEC Signing

Sign zone responses with DNSSEC keys. This is the strongest signal to public resolvers that responses are authentic:

- Support NSEC for authenticated denial of existence
- Zone signing with RSA/ECDSA keys
- Automatic key rotation support
- DS record generation for parent zone delegation

Configuration:
```
NORTHSTAR_DNSSEC_ENABLE=1
NORTHSTAR_DNSSEC_KEY="/path/to/Kexample.net.+013+12345.key"
NORTHSTAR_DNSSEC_PRIVATE="/path/to/Kexample.net.+013+12345.private"
```

This is complex and should be approached carefully. Start with basic signing, add NSEC later.

**Files**: New `pkg/dnssec/dnssec.go`, integration in `pkg/backend/backend.go`

### 5.4 Response Caching

Cache responses for frequently queried records to reduce hashmap lookup overhead under high load:

- LRU cache keyed on (domain, type, class)
- TTL-aware expiration
- Cache invalidation on zone reload
- Configurable cache size

**Files**: New `pkg/cache/cache.go`, integration in `pkg/backend/backend.go`

### 5.5 Connection Pooling for Upstream

Pool TCP/TLS connections to upstream resolvers for CNAME chasing instead of opening a new connection per lookup.

**Files**: `pkg/backend/upstream.go`

---

## Phase 6: Modernization

**Goal**: Update dependencies, tooling, and code quality.

### 6.1 Go Version Update

- Update `go.mod` to `go 1.22` (or latest stable)
- Update Dockerfile base from `golang:1.17-alpine` to `golang:1.22-alpine`
- Replace deprecated `ioutil.ReadDir` with `os.ReadDir`
- Use `slog` (structured logging from stdlib) or update logrus

### 6.2 AWS SDK v2

Migrate from `aws-sdk-go` v1 to v2:
- Better performance (reduced allocations)
- Context support for cancellation
- Native credential providers (IRSA, ECS task role)
- This matters for Mulga Spinifex's S3-compatible Predastore backend

### 6.3 S3 Event-Driven Sync

Replace polling with S3 event notifications via SQS/SNS or S3-compatible webhook:
- Instant zone reload on change instead of 60-second delay
- Lower S3 API costs
- For Mulga Spinifex's Predastore, implement a webhook receiver

### 6.4 Predastore Backend Support

Since Mulga Spinifex uses Predastore (S3-compatible), ensure northstar works with any S3-compatible endpoint:

```
NORTHSTAR_S3_ENDPOINT="https://predastore.spinifex.spx3.net:8443"
ZONE_DIR="s3://dns-zones"
```

This likely works already with the AWS SDK's endpoint override, but needs testing and a configuration path.

---

## Implementation Priority

**Immediate (fix public resolution)**:
1. Phase 1.1 - TCP listener
2. Phase 1.2 - EDNS0 support
3. Phase 1.3 - Fix response codes
4. Phase 1.4 - NS authority records
5. Phase 1.5 - Fix SOA serial
6. Phase 1.6 - Fix RWMutex
7. Phase 1.7 - Graceful shutdown

**Short-term (encrypted DNS + Mulga Spinifex basics)**:
8. Phase 2.1 - DNS-over-TLS
9. Phase 2.3 - Encrypted upstream
10. Phase 3.1 - SRV records
11. Phase 4.1 - Dynamic record API
12. Phase 3.4 - Wildcard records

**Medium-term (Mulga Spinifex production readiness)**:
13. Phase 4.2 - Split-horizon DNS
14. Phase 4.3 - Health-aware DNS
15. Phase 5.1 - Prometheus metrics
16. Phase 5.2 - Rate limiting
17. Phase 3.2 - CAA records
18. Phase 3.3 - PTR records
19. Phase 6.1 - Go version update
20. Phase 6.4 - Predastore backend

**Long-term (hardening)**:
21. Phase 2.2 - DNS-over-HTTPS
22. Phase 5.3 - DNSSEC signing
23. Phase 5.4 - Response caching
24. Phase 6.2 - AWS SDK v2
25. Phase 6.3 - S3 event-driven sync

---

## Testing Strategy

Each phase should include:

- **Unit tests** for new handler cases (miekg/dns has excellent test utilities)
- **Integration tests** using `dig`, `kdig` (knot-dns), and `dnspython` to validate RFC compliance
- **Compliance test**: Run [DNS Compliance Testing](https://ednscomp.isc.org/) against northstar after Phase 1
- **Benchmark updates**: Extend existing benchmarks for new record types and TCP/TLS paths
- **Race detection**: `make race` after every mutex-related change

### Validation Commands

After Phase 1, these should all work correctly:
```bash
# TCP query
dig @ns1.spx3.net spx3.net A +tcp

# EDNS0 query
dig @ns1.spx3.net spx3.net A +edns=0 +bufsize=4096

# Verify SOA serial increments on zone change
dig @ns1.spx3.net spx3.net SOA +short

# Verify NS in authority section
dig @ns1.spx3.net spx3.net A +noall +authority

# Verify NXDOMAIN for non-existent names
dig @ns1.spx3.net nonexistent.spx3.net A  # expect NXDOMAIN, not REFUSED

# Verify from public resolvers
dig @8.8.8.8 spx3.net A
dig @1.1.1.1 spx3.net A
```
