<p align="center">
  <img 
      src=".github/assets/banner.svg" 
      alt="Northstar by Mulga — lightweight authoritative DNS with UDP, TCP and DNS-over-TLS support, in-memory lookups and zones stored locally or in S3.” 
      width="900"
  >
</p>

<p align="center">
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-AGPL--3.0-3fb950?style=flat-square" alt="License"></a>
  <a href="https://mulgadc.com"><img src="https://img.shields.io/badge/Home-mulga-orange?style=flat-square&logo=data:image/svg%2bxml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIGZpbGw9IiNmZmYiIHZpZXdCb3g9IjAgMCAyNCAyNCI+PHBhdGggZD0iTTE2LjcxOCA4Ljg5MWMtMS4yODUgMS4zNy0zLjE3OCAyLjMxNy00LjY5NSAzLjQ0LS44NTQuNjMtMy4wODkgMi4yNy0xLjYxIDMuMzQ0IDEuNzg4IDEuMjk4IDYuMjQzLjE2NCA3Ljc4NS0xLjI2Ljg0LS43NzUuODE0LTEuODIyLS4zMjgtMi4yNTktMS4yMzUtLjQ3Mi0yLjY2LS4xMTEtMy45MTMuMDg3LS4wNDIuMDA3LS4xMzEuMDU1LS4xMjMtLjAyLjUyMy0uMzM2Ljk5My0uNzUgMS41MDMtMS4xMDIuNDkyLS4zNCAxLjA5Ny0uODI3IDEuNy0uODM4IDEuODcxLS4wMzQgMy43OTkuODkgNC4yODcgMi44MTUuODExIDMuMjAzLTMuMDA2IDUuNzE1LTUuNzg0IDUuOTQybDEuNjE0LS43NDdjLjYwNS0uMzYgMS4yMTctLjczNCAxLjc1Mi0xLjE5Ni4xMzMtLjExNS4yMy0uMjYzLjM0Mi0uMzczLjAyNy0uMDI2LjMwMi0uMTQ0LjE0NC0uMTUtMS40NDEuOTQ1LTMuMTI3IDEuNTcyLTQuODEzIDEuOTMyLS41OC4xMjMtMS4xOTUuMjQyLTEuNzg1LjI1Ny4wMTUuMDguMDg3LjA2NC4xNC4wOC40NzYuMTU0IDEuMDIuMjQ1IDEuNTE2LjMyLjA2OC4wNTItLjAwNi4wNzQtLjA2LjA4MS0uNDQ4LjA1OC0uOTIzLjE0My0xLjM3LjE2Ni0xLjI3LjA2NC0yLjU1LS4wNjgtMy43NzctLjM4bC0uNjktMS45NTdjLS4wNi4wMDYtLjA2My4wNi0uMDc5LjEwMy0uMDYuMTctLjMwNSAxLjQxNy0uMzg3IDEuNDM1LS42Ni0uMi0xLjI1MS0uNjQ3LTEuNzM2LTEuMTMybDEuMTUtMi4wNi0xLjgzNSAxLjAxMWMtLjI1OC0uNTgzLS4zMDUtMS4yNDMtLjIxOS0xLjg3bDIuMzc1LTEuMThjLS43MzktLjA0OS0xLjQ4Ni4wNjgtMi4yMi4xNDUuMTI1LS4zNi4yNzctLjcxOC40NjItMS4wNTEuMDY3LS4xMjIuNDI2LS43MjEuNTItLjcyNC44Ni4xNjQgMS43ODYuMjQxIDIuNjQ4LjA3NGwtMS44Ny0uNzcxLS4wNjktLjA5M2MuMzgyLS4zNi43Ny0uNzE3IDEuMTk5LTEuMDI0LjgwNC4yMzQgMS42My40MjIgMi40NzMuNDMzTDkuMzUgOS4zNDJhNyA3IDAgMCAxIC41NjctLjQwMWMuMTYtLjEwMy42MDktLjQwMi43NzItLjM4LjY1LjIyMSAxLjMyNC4zNzggMi4wMS40MzMtLjMxMy0uMzI1LS45MDktLjU2NC0xLjIxLS44NjgtLjAzMy0uMDM0LS4wNTgtLjAzNi0uMDQzLS4wOTguNzg3LS40NTIgMS42NjItLjkwMSAyLjM0LTEuNTE3LjY5LS42MjkgMS40MjEtMS42NTYuOTI0LTIuNjA1LjU3My4xODIgMS4wNjcuOTA2IDEuMDU0IDEuNTEyLS4wMzQgMS42NzYtMS44MjIgMy4xNy0yLjk0MyA0LjIyMiAxLjczMi0uNzI4IDMuNzE0LTIuMjMgMy43MS00LjMwNS0uMDAzLTEuNjg4LTEuNTQtMi4zNjUtMi45OTMtMi41MThhMy4yIDMuMiAwIDAgMS0uMzg1IDEuMDg3Yy0uNDI4LjcxOS0xLjMwMiAxLjE2OC0xLjc1OCAxLjkxNS0uMzExLjUxLS4zNyAxLjE5NS0xLjAzMSAxLjM5LS4yNy4wOC0uNjE3LjA5My0uODk3LjA5NGwuNjE1LS4zMjVjLjY4OS0uNTA2LjY3Ny0xLjQyIDEuMDk5LTIuMS0uMDUtLjA3LS41NDUtLjItLjY2LS4yMjktLjUxLS4xMjUtMS4zNzUtLjI4NS0xLjg4Ni0uMjUtLjE1Ny4wMS0uODY5LjEyMS0uOTMuMjQyLS4wODguMTc3LjM0MS45My40NjggMS4xMDEuMDI1LjAzNS4wNzcuMDI2LjA4LjAzLjAyNC4wMzEuMDA3LjEwNS0uMDYuMDhhNSA1IDAgMCAxLS40MDQtLjI4M2MtLjUwMy0uMzg4LTEuMDc4LS45NzQtMS40OTktMS40NDctLjA2OC0uMTY2LS4xNi0uMzUuMDAyLS40ODcuODg4LS41NSAxLjc2OC0xLjEyNiAyLjY3LTEuNjUxIDIuNjcxLTEuNTU4IDUuNzExLTMuMzE4IDguMjY3LS40NjUgMi4xMTkgMi4zNjYgMS41MTQgNS4yMTItLjUxMiA3LjM3MnptLTguMjI0LTUuMzhjLjY5OS0uMTIyIDIuMDE4LjU3MiAyLjIzNS0uNDU2LjAyMS0uMS0uMDM2LS4xNTcuMDItLjI1NS4wNDMtLjA3My4yODYtLjI1LjM3LS4zMTguMjctLjIxNy41NzctLjM4Ny44NDItLjYxMi0xLjAyOS4xNjYtMi4wNjUuNzU2LTIuOTY0IDEuMjc3LS4xNDkuMDg2LS4zMS4xNzYtLjQ1MS4yNzMtLjAzNy4wMjUtLjA3NS0uMDA2LS4wNTMuMDltLTQuODMgMTAuODQ0Yy0xLjcxNSAxLjg4MS0xLjMyNSA0LjU3LjQ5NCA2LjIxNyAxLjgxMSAxLjY0MSA0LjY2IDIuMjIzIDcuMDQ4IDIuMTA4IDIuMTUyLS4xMDMgNC4zMzctLjgxMiA2LjQ2LS4wOTEuODI1LjI4IDEuNTQ2LjgwNSAyLjE2IDEuNDExLS4wODMtLjQtLjMyNC0uODE5LS41NTgtMS4xNTktMi4xMy0zLjA5NS02LjI3LTEuOTM1LTkuNDI2LTIuNTUzLTEuNzExLS4zMzUtMy40OTEtMS4xMTgtNC41MzMtMi41NjctLjkwMS0xLjI1My0xLjA0Mi0yLjczLS41OTUtNC4xOTQtLjA5MS0uMDktLjk1Mi43MjEtMS4wNS44MjhtNi4zNjYtOC42NjdjLS4zODIuMzM5LS43ODcuNjYtMS4yMTIuOTQ4bC0xLjIwNy41OWMxLjAyMi4wMzkgMi4wOC0uNTQ2IDIuNDItMS41MzciLz48L3N2Zz4=" alt="mulgadc.com"></a>
</p>

<p align="center">
  <a href="#why-northstar">Why Northstar?</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#protocol-and-record-support">Protocol support</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#zone-file-format">Zone files</a> ·
  <a href="#spinifex-integration">Spinifex integration</a> ·
  <a href="#docker">Docker</a> ·
  <a href="#development">Development</a> ·
  <a href="#roadmap">Roadmap</a> ·
  <a href="https://docs.mulgadc.com">Docs</a>
</p>

---

# Northstar: Fast, lightweight authoritative DNS for infrastructure you control.

Northstar is an authoritative DNS server written in Go. It serves zones from local TOML files or S3-compatible storage over UDP, TCP and DNS-over-TLS.

It can run independently or provide authoritative DNS and service discovery for a Spinifex installation.

## Why Northstar?

- **Self-hosted DNS done right** — Run your own authoritative nameserver without the operational complexity of BIND or PowerDNS. Zone files are human-readable TOML, configuration is environment variables, and the whole thing deploys as a single container.
- **S3-native zone management** — Store zone files in AWS S3, [Predastore](https://github.com/mulgadc/predastore/), MinIO, or any S3-compatible backend. Northstar syncs automatically, so you can manage DNS records through the same object storage pipeline as the rest of your infrastructure.
- **Built for [Spinifex](https://github.com/mulgadc/spinifex)** — Northstar serves as the DNS backbone for Spinifex, an open-source AWS alternative. It handles both internal service discovery (SRV records for NATS, gateways, and other cluster services) and public-facing authoritative DNS, all from the same instance.
- **Plays nice with public resolvers** — Full RFC compliance means Cloudflare (1.1.1.1), Google (8.8.8.8), and every other recursive resolver can properly resolve your domains. TCP fallback, EDNS0, correct NXDOMAIN/NODATA semantics, proper authority sections — the things that matter when your DNS needs to actually work on the real internet.

## Quick start

```bash
git clone https://github.com/mulgadc/northstar
cd northstar
make build
ZONE_DIR="./config/domains" ./bin/northstar
```

Query the running server:

```bash
dig @127.0.0.1 hello_a.net A
dig @127.0.0.1 hello_a.net A +tcp
dig @127.0.0.1 hello_a.net A +edns=0
```
## Capabilities

- Authoritative DNS over UDP and TCP
- DNS-over-TLS
- EDNS0
- Local TOML zone files
- S3-compatible zone storage
- Filesystem reload and periodic S3 synchronisation
- Wildcard records with exact-match priority
- Configurable upstream resolvers with TLS and failover
- Graceful shutdown
- Container deployment and single-binary distribution

## Protocol and record support

### Protocols

- DNS over UDP
- DNS over TCP
- DNS over TLS
- EDNS0

### Record types

| Type | Code | Primary fields |
| --- | ---: | --- |
| A | 1 | `address` (IPv4) |
| NS | 2 | `address` (nameserver FQDN) |
| CNAME | 5 | `address` (target FQDN) |
| SOA | 6 | Generated from the `[domain]` section |
| PTR | 12 | `address` (target FQDN) |
| MX | 15 | `address`, `preference` |
| TXT | 16 | `address` (text value) |
| AAAA | 28 | `address` (IPv6) |
| SRV | 33 | `address`, `priority`, `weight`, `port` |
| CAA | 257 | `address`, `caa_flag`, `caa_tag` |

## Architecture

<p align="center">
  <img src=".github/assets/platform.svg" alt="Northstar: resolvers and infrastructure services on top, authoritative DNS over UDP, TCP, and DNS-over-TLS, with fast in-memory zones loaded from local files or S3-compatible storage." width="900">
</p>

Northstar loads authoritative zone data into an in-memory lookup structure.
Zones can be read from the local filesystem or retrieved from an S3-compatible
backend. Requests are accepted over UDP, TCP or DNS-over-TLS and answered from
the authoritative zone store.

Optional upstream resolvers can be configured with TLS and failover for CNAME
chasing.

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
| `NORTHSTAR_S3_INSECURE` | | Standalone environment mode only: skip S3 TLS certificate verification |
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

Mulga's S3-compatible storage ([Predastore](https://github.com/mulgadc/predastore)) can serve as the zone file backend, keeping DNS configuration alongside the rest of the Spinifex infrastructure. Northstar verifies the endpoint certificate using the system trust store; install a private CA there or provide it with `SSL_CERT_FILE` / `SSL_CERT_DIR`:

```sh
ZONE_DIR="s3://dns-zones" \
NORTHSTAR_S3_ENDPOINT="https://predastore.spinifex.spx3.net:8443" \
SSL_CERT_FILE="/path/to/spinifex-ca.pem" \
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

## Development

```sh
make test          # Unit tests
make test-race     # Unit tests with the race detector
make test-cover    # Unit tests with coverage (fails below threshold)
make lint          # golangci-lint (use `make fix` to auto-fix)
make govulncheck   # Dependency vulnerability scan
make bench         # Benchmarks
make e2e           # E2E tests via Docker (Predastore + Northstar)
make preflight     # lint + govulncheck + coverage + race
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

Roadmap items describe direction and are not commitments to a release date.

## License

Northstar is licensed under the [GNU Affero General Public License v3.0 (AGPLv3)](LICENSE) license.
