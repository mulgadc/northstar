# Northstar

**Fast, lightweight authoritative DNS server built for infrastructure you own.**

Northstar is a production-grade DNS server written in Go that gives you full control over your DNS infrastructure — no third-party API, no vendor dashboard, no per-query pricing. Define your zones in simple TOML files, store them on disk or in any S3-compatible object store, and let Northstar handle the rest.

It speaks UDP, TCP, and DNS-over-TLS (DoT), responds in ~160 microseconds, and fits in a single static binary. Whether you're running a handful of domains on a VPS or powering service discovery across a distributed cluster, Northstar is designed to stay out of your way and just work.

### Why Northstar?

- **Self-hosted DNS done right** — Run your own authoritative nameserver without the operational complexity of BIND or PowerDNS. Zone files are human-readable TOML, configuration is environment variables, and the whole thing deploys as a single container.
- **S3-native zone management** — Store zone files in AWS S3, [Predastore](https://github.com/mulgadc/predastore/), MinIO, or any S3-compatible backend. Northstar syncs automatically, so you can manage DNS records through the same object storage pipeline as the rest of your infrastructure.
- **Built for [Spinifex](https://github.com/mulgadc/spinifex)** — Northstar serves as the DNS backbone for Spinifex, an open-source AWS alternative. It handles both internal service discovery (SRV records for NATS, gateways, and other cluster services) and public-facing authoritative DNS, all from the same instance.
- **Plays nice with public resolvers** — Full RFC compliance means Cloudflare (1.1.1.1), Google (8.8.8.8), and every other recursive resolver can properly resolve your domains. TCP fallback, EDNS0, correct NXDOMAIN/NODATA semantics, proper authority sections — the things that matter when your DNS needs to actually work on the real internet.

## Features

- **UDP + TCP + DNS-over-TLS** on configurable ports
- **EDNS0** support for modern resolver compatibility
- **10 record types** — A, AAAA, CNAME, MX, NS, TXT, SOA, SRV, CAA, PTR
- **Wildcard records** with exact-match priority
- **In-memory hashmap** for O(1) lookups (~160µs per query)
- **Zone files in TOML** format, loaded from local filesystem or S3
- **Live reload** — filesystem watch (fsnotify) or periodic S3 sync
- **S3-compatible backends** — works with AWS S3, [Predastore](https://github.com/mulgadc/predastore), MinIO, etc.
- **Correct RFC semantics** — NXDOMAIN, NODATA, REFUSED, NS authority section, zone-based SOA serial
- **Configurable upstream resolvers** with TLS and failover for CNAME chasing
- **Graceful shutdown** on SIGTERM/SIGINT
- **Container-first** — multi-arch Docker images, single binary

## Quick Start

```sh
git clone https://github.com/mulgadc/northstar
cd northstar
make build
ZONE_DIR="./config/domains" ./bin/northstar
```

Verify it works:

```sh
dig @127.0.0.1 hello_a.net A
dig @127.0.0.1 hello_a.net MX
dig @127.0.0.1 hello_a.net TXT
dig @127.0.0.1 hello_a.net A +tcp      # TCP query
dig @127.0.0.1 hello_a.net A +edns=0   # EDNS0 query
```

## Configuration

All configuration is via environment variables.

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `ZONE_DIR` | `config/domains/` | Path to zone files or `s3://bucket-name` |
| `HOST` | `0.0.0.0` | Listen address |
| `PORT` | `53` | Listen port (UDP + TCP) |
| `NORTHSTAR_LOG_IGNORE` | | Suppress all logging |
| `NORTHSTAR_LOG_DEBUG` | | Enable debug logging |

### DNS-over-TLS

| Variable | Default | Description |
|----------|---------|-------------|
| `NORTHSTAR_TLS_CERT` | | Path to TLS certificate (PEM) |
| `NORTHSTAR_TLS_KEY` | | Path to TLS private key |
| `DOT_PORT` | `853` | DoT listener port |

### S3 / S3-Compatible Storage

| Variable | Default | Description |
|----------|---------|-------------|
| `AWS_ACCESS_KEY` | | AWS access key ID |
| `AWS_SECRET_ACCESS_KEY` | | AWS secret access key |
| `AWS_REGION` | | AWS region |
| `NORTHSTAR_S3_ENDPOINT` | | Custom S3 endpoint URL (for Predastore, MinIO, etc.) |
| `NORTHSTAR_S3_INSECURE` | | Skip TLS verification for self-signed certs |
| `S3_SYNC_RETRY` | `60` | S3 sync interval in seconds |

### Upstream Resolvers

| Variable | Default | Description |
|----------|---------|-------------|
| `NORTHSTAR_UPSTREAM` | `tls://1.1.1.1:853,tls://8.8.8.8:853,1.1.1.1:53` | Comma-separated upstream servers for CNAME chasing. Prefix with `tls://` for DNS-over-TLS. |

## Zone File Format

Zone files use TOML. Each file represents one zone and is named `<domain>.toml`.

```toml
version = 1.0

[domain]
domain = "example.com"
soa = "ns1.example.com."
created = 2024-01-01T00:00:00Z
modified = 2024-06-15T12:00:00Z
verified = true
active = true
ownerid = 1

[defaults]
ttl = 3600
type = 1    # A record
class = 1   # IN

# A records
[[records]]
domain = ""
address = "203.100.1.1"

[[records]]
domain = "www."
address = "203.100.1.1"

# Wildcard — matches any subdomain without an explicit record
[[records]]
domain = "*."
address = "203.100.1.99"

# NS records
[[records]]
domain = ""
type = 2
address = "ns1.example.com."

[[records]]
domain = ""
type = 2
address = "ns2.example.com."

# MX records
[[records]]
domain = ""
type = 15
preference = 10
address = "mail.example.com."

# TXT records (SPF, DKIM, verification, etc.)
[[records]]
domain = ""
type = 16
address = "v=spf1 mx a -all"

# AAAA record
[[records]]
domain = ""
type = 28
address = "2001:db8::1"

# SRV record (service discovery)
[[records]]
domain = "_nats._tcp."
type = 33
priority = 10
weight = 0
port = 4222
address = "node1.example.com."

# CAA record (certificate authority authorization)
[[records]]
domain = ""
type = 257
caa_flag = 0
caa_tag = "issue"
address = "letsencrypt.org"

# PTR record (reverse DNS — in a separate zone file for in-addr.arpa)
# [[records]]
# domain = "1."
# type = 12
# address = "host-1.example.com."
```

### Record Type Reference

| Type | Code | Fields |
|------|------|--------|
| A | 1 | `address` (IPv4) |
| NS | 2 | `address` (nameserver FQDN) |
| CNAME | 5 | `address` (target FQDN) |
| SOA | 6 | Auto-generated from `[domain]` section |
| PTR | 12 | `address` (target FQDN) |
| MX | 15 | `address` (mail server FQDN), `preference` |
| TXT | 16 | `address` (text value) |
| AAAA | 28 | `address` (IPv6) |
| SRV | 33 | `address` (target FQDN), `priority`, `weight`, `port` |
| CAA | 257 | `address` (CA domain), `caa_flag`, `caa_tag` |

## Spinifex Integration

Northstar serves as the DNS layer for [Spinifex](https://github.com/mulgadc/spinifex), providing both internal service discovery and public authoritative DNS.

**Service discovery with SRV records:**

```toml
# _nats._tcp.spinifex.spx3.net → node1.spinifex.spx3.net:4222
[[records]]
domain = "_nats._tcp.spinifex."
type = 33
priority = 10
weight = 0
port = 4222
address = "node1.spinifex.spx3.net."

# _awsgw._tcp.spinifex.spx3.net → node1.spinifex.spx3.net:9999
[[records]]
domain = "_awsgw._tcp.spinifex."
type = 33
priority = 10
weight = 0
port = 9999
address = "node1.spinifex.spx3.net."
```

**Using Predastore as the zone file backend:**

Mulga's S3-compatible storage ([Predastore](https://github.com/mulgadc/predastore)) can serve as the zone file backend, keeping DNS configuration alongside the rest of the Spinifex infrastructure:

```sh
ZONE_DIR="s3://dns-zones" \
NORTHSTAR_S3_ENDPOINT="https://predastore.spinifex.spx3.net:8443" \
NORTHSTAR_S3_INSECURE=1 \
AWS_ACCESS_KEY="..." \
AWS_SECRET_ACCESS_KEY="..." \
AWS_REGION="us-west-1" \
./bin/northstar
```

## Docker

**Docker Compose (S3):**

```sh
AWS_ACCESS_KEY="X" AWS_SECRET_ACCESS_KEY="Y" ZONE_DIR="s3://my-bucket" AWS_REGION="us-west-1" docker compose up -d
```

**Standalone (filesystem):**

```sh
docker run \
  --mount src=./config/domains,target=/config/domains,type=bind \
  -e ZONE_DIR="/config/domains" \
  -p 53:53/udp -p 53:53/tcp \
  calacode/northstar-dns
```

**With DNS-over-TLS:**

```sh
docker run \
  --mount src=./config/domains,target=/config/domains,type=bind \
  --mount src=./certs,target=/certs,type=bind \
  -e ZONE_DIR="/config/domains" \
  -e NORTHSTAR_TLS_CERT="/certs/server.pem" \
  -e NORTHSTAR_TLS_KEY="/certs/server.key" \
  -p 53:53/udp -p 53:53/tcp -p 853:853/tcp \
  calacode/northstar-dns
```

## Testing

```sh
make test          # Unit tests (31 tests)
make race          # Race condition detection
make bench         # Benchmarks with benchstat
make e2e           # E2E tests via Docker (Predastore + Northstar)
make test-all      # Unit tests + race detection
```

## Benchmarking

```sh
make bench
```

Simulates 26 domains with ~255 subdomains each:

```
name           time/op
DNSQueryA-8     160µs ±12%
DNSQueryTXT-8   172µs ±19%
DNSQueryMX-8    162µs ±12%

name           alloc/op
DNSQueryA-8    3.09kB ± 0%
DNSQueryTXT-8  3.68kB ± 0%
DNSQueryMX-8   4.05kB ± 0%
```

## Roadmap

See [DEV.md](DEV.md) for the full development plan.

- [ ] DNS-over-HTTPS (DoH)
- [ ] DNSSEC signing
- [ ] Prometheus metrics endpoint
- [ ] Rate limiting / DDoS protection
- [ ] Dynamic record API (HTTP)
- [ ] Split-horizon DNS (internal vs external views)
- [ ] Health-aware DNS responses
- [ ] Response caching

## License

Northstar is licensed under the GNU Affero General Public License v3.0 (AGPLv3). See [LICENSE](LICENSE) for the full text.
