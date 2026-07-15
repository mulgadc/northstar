package config

import (
	"fmt"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDomainLookup(t *testing.T) {
	conf := GenerateTestDomains(1000)

	assert.Len(t, conf.Domain, 1000)

	for i := range 1000 {
		lookup := DomainLookup{Domain: fmt.Sprintf("test%d.net", i), Type: 1, Class: 1}
		assert.Len(t, conf.Records[lookup], 4)

		for i2 := 1; i2 < 5; i2++ {
			if len(conf.Records[lookup]) == 4 {
				assert.Equal(t, fmt.Sprintf("213.189.1.%d", i2), conf.Records[lookup][i2-1].Address)
			}
		}
	}

	// Test delete
	conf.DeleteZone("test1.net")
	lookup := DomainLookup{Domain: "test1.net", Type: 1, Class: 1}
	assert.Empty(t, conf.Records[lookup])
	assert.Empty(t, conf.Domain["test1.net"].Domain)
}

func TestConfigGood(t *testing.T) {
	file := `
# This is a TOML document. Boom.
version = 1.1

[domain]
domain = "hotastest.net"
created = 2021-05-27T07:32:00Z
modified = 2022-05-27T07:32:00Z
verified = true
active = true
ownerid = 10

[defaults]
ttl = 3600
type = 1
class = 1

[[records]]
domain = "web3.defi."
address = "213.189.1.4"

[[records]]
domain = "www."
type = 2
class = 1
address = "e15316.a.akamaiedge.net."
`

	config := ConfigArr{}
	toml.Unmarshal([]byte(file), &config)
	ApplyDefaults(&config, time.Now())

	assert.Equal(t, "hotastest.net", config.Domain.Domain)

	assert.Equal(t, "web3.defi.hotastest.net.", config.Records[0].Domain)
	assert.Equal(t, "213.189.1.4", config.Records[0].Address)
	assert.Equal(t, uint16(1), config.Records[0].Type)
	assert.Equal(t, uint16(1), config.Records[0].Class)
	assert.Equal(t, uint32(3600), config.Records[0].TTL)

	assert.Equal(t, "www.hotastest.net.", config.Records[1].Domain)
	assert.Equal(t, uint16(2), config.Records[1].Type)
	assert.Equal(t, uint16(1), config.Records[1].Class)
	assert.Equal(t, "e15316.a.akamaiedge.net.", config.Records[1].Address)
}

func TestConfigDefaults(t *testing.T) {
	file := `
# This is a TOML document. Boom.
version = 1.1

[domain]
domain = "nodefaults.net"
verified = true
active = true

[[records]]
domain = "web3.defi."
address = "213.189.1.4"
`

	config := ConfigArr{}
	toml.Unmarshal([]byte(file), &config)
	ApplyDefaults(&config, time.Now())

	assert.Equal(t, "nodefaults.net", config.Domain.Domain)
	assert.Equal(t, uint16(1), config.Records[0].Type)
	assert.Equal(t, uint16(1), config.Records[0].Class)
	assert.Equal(t, uint32(3600), config.Records[0].TTL)
}

func TestConfigBad(t *testing.T) {
	file := `
# This is a TOML document. Boom.
versionz = 1.1

[domainz]
domain = "bad.domain.net"

[[norecords]]
domain = "bad.defi."

[[norecords]]
domain = "www."
`

	config := ConfigArr{}
	toml.Unmarshal([]byte(file), &config)
	ApplyDefaults(&config, time.Now())

	assert.Empty(t, config.Domain.Domain)
	assert.Empty(t, config.Records)
}

func TestConfigSRV(t *testing.T) {
	file := `
version = 1.0

[domain]
domain = "srvtest.net"
created = 2021-05-27T07:32:00Z
modified = 2022-05-27T07:32:00Z
verified = true
active = true
ownerid = 10

[defaults]
ttl = 300
type = 1
class = 1

[[records]]
domain = "_nats._tcp."
type = 33
priority = 10
weight = 5
port = 4222
address = "node1.srvtest.net."
`

	config := ConfigArr{}
	err := toml.Unmarshal([]byte(file), &config)
	require.NoError(t, err)
	ApplyDefaults(&config, time.Now())

	assert.Equal(t, "srvtest.net", config.Domain.Domain)
	require.Len(t, config.Records, 1)
	assert.Equal(t, uint16(33), config.Records[0].Type)
	assert.Equal(t, uint16(10), config.Records[0].Priority)
	assert.Equal(t, uint16(5), config.Records[0].Weight)
	assert.Equal(t, uint16(4222), config.Records[0].Port)
	assert.Equal(t, "node1.srvtest.net.", config.Records[0].Address)
	assert.Equal(t, "_nats._tcp.srvtest.net.", config.Records[0].Domain)
}

func TestConfigCAA(t *testing.T) {
	file := `
version = 1.0

[domain]
domain = "caatest.net"
created = 2021-05-27T07:32:00Z
modified = 2022-05-27T07:32:00Z
verified = true
active = true

[defaults]
ttl = 3600
type = 1
class = 1

[[records]]
domain = ""
type = 257
caa_flag = 0
caa_tag = "issue"
address = "letsencrypt.org"
`

	config := ConfigArr{}
	err := toml.Unmarshal([]byte(file), &config)
	require.NoError(t, err)
	ApplyDefaults(&config, time.Now())

	require.Len(t, config.Records, 1)
	assert.Equal(t, uint16(257), config.Records[0].Type)
	assert.Equal(t, uint8(0), config.Records[0].CAAFlag)
	assert.Equal(t, "issue", config.Records[0].CAATag)
	assert.Equal(t, "letsencrypt.org", config.Records[0].Address)
}

func TestFindZone(t *testing.T) {
	conf := Config{
		Records: make(map[DomainLookup][]Records),
		Domain:  make(map[string]Domain),
	}
	conf.Domain["example.com"] = Domain{Domain: "example.com"}
	conf.Domain["sub.example.com"] = Domain{Domain: "sub.example.com"}

	// Direct match
	zone, found := conf.FindZone("example.com.")
	assert.True(t, found)
	assert.Equal(t, "example.com", zone)

	// Subdomain match
	zone, found = conf.FindZone("foo.example.com.")
	assert.True(t, found)
	assert.Equal(t, "example.com", zone)

	// Deeper subdomain
	zone, found = conf.FindZone("bar.foo.example.com.")
	assert.True(t, found)
	assert.Equal(t, "example.com", zone)

	// More specific zone match
	zone, found = conf.FindZone("test.sub.example.com.")
	assert.True(t, found)
	assert.Equal(t, "sub.example.com", zone)

	// Non-existent zone
	_, found = conf.FindZone("unknown.net.")
	assert.False(t, found)
}

func TestNameExists(t *testing.T) {
	conf := Config{
		Records: make(map[DomainLookup][]Records),
		Domain:  make(map[string]Domain),
	}

	conf.Records[DomainLookup{Domain: "example.com.", Type: 1, Class: 1}] = []Records{
		{Domain: "example.com.", Type: 1, Class: 1, Address: "1.2.3.4"},
	}

	assert.True(t, conf.NameExists("example.com."))
	assert.False(t, conf.NameExists("nonexistent.com."))
}

func TestAddDeleteZoneConcurrent(t *testing.T) {
	conf := Config{
		Records: make(map[DomainLookup][]Records),
		Domain:  make(map[string]Domain),
	}

	// Add zones concurrently
	done := make(chan bool, 100)
	for i := range 100 {
		go func(idx int) {
			zone := ConfigArr{
				Domain: Domain{
					Domain: fmt.Sprintf("test%d.net", idx),
					SOA:    fmt.Sprintf("ns.test%d.net", idx),
				},
				Records: []Records{
					{Domain: fmt.Sprintf("test%d.net.", idx), Type: 1, Class: 1, TTL: 3600, Address: "1.2.3.4"},
				},
			}
			conf.AddZone(zone)
			done <- true
		}(i)
	}

	for range 100 {
		<-done
	}

	assert.Len(t, conf.Domain, 100)

	// Delete zones concurrently
	for i := range 100 {
		go func(idx int) {
			conf.DeleteZone(fmt.Sprintf("test%d.net", idx))
			done <- true
		}(i)
	}

	for range 100 {
		<-done
	}

	assert.Empty(t, conf.Domain)
}

func TestCheckConfigDomainMatch(t *testing.T) {
	// Match
	err := checkConfigDomainMatch("hello.net.toml", "hello.net")
	assert.NoError(t, err)

	// Match with path
	err = checkConfigDomainMatch("/path/to/hello.net.toml", "hello.net")
	assert.NoError(t, err)

	// Mismatch
	err = checkConfigDomainMatch("hello.net.toml", "other.net")
	assert.Error(t, err)
}
