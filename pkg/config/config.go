package config

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/fsnotify/fsnotify"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Records map[DomainLookup][]Records
	Domain  map[string]Domain
	Mu      sync.RWMutex
}

type ConfigArr struct {
	Version  float32
	Domain   Domain
	Defaults Defaults
	Records  []Records
}

type Domain struct {
	Domain    string
	SOA       string
	Created   time.Time
	Modified  time.Time
	Verified  bool
	Active    bool
	OwnerID   uint32
	RecordRef []DomainLookup
}

type Defaults struct {
	TTL   uint32
	Type  uint16
	Class uint16
}

type Records struct {
	Domain     string
	Root       string
	TTL        uint32
	Type       uint16
	Class      uint16
	Preference uint16
	Address    string
	// SRV record fields (RFC 2782)
	Priority uint16
	Weight   uint16
	Port     uint16
	// CAA record fields (RFC 8659)
	CAAFlag uint8  `toml:"caa_flag"`
	CAATag  string `toml:"caa_tag"`
}

type DomainLookup struct {
	Domain string
	Type   uint16
	Class  uint16
}

// S3Config carries explicit credentials and endpoint for an S3-compatible zone
// backend (e.g. predastore). A nil S3Config means filesystem-only operation.
// This replaces the former environment-driven global session so the library can
// be embedded without reading process env or holding package-level state.
type S3Config struct {
	Endpoint  string `toml:"endpoint"`
	Region    string `toml:"region"`
	Bucket    string `toml:"bucket"`
	AccessKey string `toml:"access_key"`
	SecretKey string `toml:"secret_key"`
	Insecure  bool   `toml:"insecure"`
}

// newS3Session builds an AWS session from explicit S3Config — no global state,
// no environment lookups.
func newS3Session(cfg *S3Config) *session.Session {
	awsCfg := aws.Config{}

	if cfg.Region != "" {
		awsCfg.Region = aws.String(cfg.Region)
	}

	if cfg.Endpoint != "" {
		awsCfg.Endpoint = aws.String(cfg.Endpoint)
		awsCfg.S3ForcePathStyle = aws.Bool(true)
	}

	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		awsCfg.Credentials = credentials.NewStaticCredentials(cfg.AccessKey, cfg.SecretKey, "")
	}

	if cfg.Insecure {
		awsCfg.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // G402: opt-in via S3Config.Insecure for self-signed S3 endpoints
			},
		}
	}

	return session.Must(session.NewSession(&awsCfg))
}

// FindZone walks up the domain labels to find which zone we are authoritative for.
// Returns the zone name and true if found, or empty string and false.
func (c *Config) FindZone(name string) (string, bool) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	// Walk up labels: "foo.bar.example.com." → "bar.example.com." → "example.com."
	labels := strings.Split(name, ".")
	for i := range labels {
		candidate := strings.Join(labels[i:], ".")
		// Strip trailing dot for domain map lookup
		stripped := strings.TrimSuffix(candidate, ".")
		if _, ok := c.Domain[stripped]; ok {
			return stripped, true
		}
	}
	return "", false
}

// NameExists checks if any records exist for a given domain name (any type).
func (c *Config) NameExists(name string) bool {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	for key := range c.Records {
		if key.Domain == name {
			return true
		}
	}
	return false
}

func GenerateTestDomains(num int) *Config {
	t := &Config{}
	t.Records = make(map[DomainLookup][]Records, 1)
	t.Domain = make(map[string]Domain, 1)

	for i := range num {
		domain := fmt.Sprintf("test%d.net", i)

		var refs []DomainLookup

		for i2 := 1; i2 < 5; i2++ {
			ip := fmt.Sprintf("213.189.1.%d", i2)
			record := DomainLookup{Domain: domain, Type: 1, Class: 1}
			t.Records[record] = append(t.Records[record], Records{Domain: domain, Address: ip})
			refs = append(refs, record)
		}

		record := DomainLookup{Domain: domain, Type: 16, Class: 1}
		t.Records[record] = append(t.Records[record], Records{Domain: domain, Address: "TESTRECORD"})
		refs = append(refs, record)

		t.Domain[domain] = Domain{Domain: domain, SOA: fmt.Sprintf("ns.%s", domain), RecordRef: refs}
	}
	return t
}

// MonitorConfig watches the zone source for changes and reloads zones live. For
// an s3:// zone_dir it polls every syncInterval (requires a non-nil s3cfg); for
// a filesystem path it uses fsnotify. It returns when ctx is cancelled, and logs
// and returns on fatal errors rather than exiting the process, so it is safe to
// run as an embedded goroutine.
func (config *Config) MonitorConfig(ctx context.Context, zone_dir string, s3cfg *S3Config, syncInterval time.Duration) {
	if syncInterval <= 0 {
		syncInterval = 30 * time.Second
	}

	if strings.HasPrefix(zone_dir, "s3://") {
		if s3cfg == nil {
			slog.Error("MonitorConfig: s3:// zone_dir requires S3 config", "zone_dir", zone_dir)
			return
		}

		sess := newS3Session(s3cfg)
		svc := s3.New(sess)

		ticker := time.NewTicker(syncInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			slog.Debug("MonitorConfig: S3 check sync state")

			path := strings.Split(zone_dir, "s3://")

			if len(path) < 2 {
				slog.Error("MonitorConfig: invalid s3:// zone_dir", "zone_dir", zone_dir)
				return
			}

			resp, err := svc.ListObjectsV2(&s3.ListObjectsV2Input{Bucket: aws.String(path[1])})

			if err != nil {
				slog.Warn("MonitorConfig: unable to list bucket", "bucket", path[1], "error", err)
				continue
			}

			configsync := make(map[string]bool, 10)

			for _, item := range resp.Contents {
				slog.Debug("MonitorConfig: scanning", "key", *item.Key)

				if !strings.HasSuffix(*item.Key, ".toml") {
					continue
				}

				domain := strings.Replace(*item.Key, ".toml", "", 1)
				configsync[domain] = true

				config.Mu.RLock()
				_, ok := config.Domain[domain]
				config.Mu.RUnlock()

				if !ok {
					myconfig, err := ReadZone(fmt.Sprintf("%s/%s", zone_dir, *item.Key), *item.LastModified, s3cfg)

					if err != nil {
						slog.Warn("MonitorConfig: read zone failed", "key", *item.Key, "error", err)
						continue
					}

					if err := checkConfigDomainMatch(*item.Key, myconfig.Domain.Domain); err == nil {
						config.AddZone(myconfig)
					} else {
						slog.Error("MonitorConfig: domain and config file mismatch, entry skipped", "domain", domain, "key", *item.Key, "error", err)
					}
				}

				config.Mu.RLock()
				domainEntry, exists := config.Domain[domain]
				config.Mu.RUnlock()

				if exists && *item.LastModified != domainEntry.Modified {
					slog.Info("MonitorConfig: new config file detected, reloading", "key", *item.Key)

					myconfig, err := ReadZone(fmt.Sprintf("%s/%s", zone_dir, *item.Key), *item.LastModified, s3cfg)

					if err != nil {
						slog.Warn("MonitorConfig: read zone failed", "key", *item.Key, "error", err)
						continue
					}

					if err := checkConfigDomainMatch(*item.Key, myconfig.Domain.Domain); err == nil {
						config.DeleteZone(domainEntry.Domain)
						config.AddZone(myconfig)
					} else {
						slog.Error("MonitorConfig: domain and config file mismatch, entry skipped", "domain", domain, "key", *item.Key, "error", err)
					}
				}
			}

			// Purge domains no longer on S3
			config.Mu.RLock()
			var toDelete []string
			for domain := range config.Domain {
				if _, ok := configsync[domain]; !ok {
					toDelete = append(toDelete, domain)
				}
			}
			config.Mu.RUnlock()

			for _, domain := range toDelete {
				slog.Debug("MonitorConfig: delete check", "domain", domain)
				config.DeleteZone(domain)
			}
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("MonitorConfig: failed to create watcher", "error", err)
		return
	}
	defer watcher.Close()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				slog.Debug("MonitorConfig: fsnotify event", "event", event)

				if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
					slog.Info("MonitorConfig: zone file changed", "name", event.Name)

					myconfig, err := ReadZone(event.Name, time.Now(), nil)
					if err != nil {
						slog.Warn("MonitorConfig: read zone failed", "name", event.Name, "error", err)
						continue
					}

					if err := checkConfigDomainMatch(event.Name, myconfig.Domain.Domain); err == nil {
						config.DeleteZone(myconfig.Domain.Domain)
						config.AddZone(myconfig)
					} else {
						slog.Error("MonitorConfig: domain and config file mismatch", "name", event.Name, "error", err)
					}
				}

				if event.Op&fsnotify.Remove == fsnotify.Remove {
					slog.Info("MonitorConfig: zone file removed", "name", event.Name)

					domain := strings.Replace(filepath.Base(event.Name), ".toml", "", 1)
					config.DeleteZone(domain)
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				slog.Warn("MonitorConfig: watcher error", "error", err)
			}
		}
	}()

	if err := watcher.Add(zone_dir); err != nil {
		slog.Error("MonitorConfig: failed to watch zone dir", "zone_dir", zone_dir, "error", err)
		return
	}

	<-ctx.Done()
}

func ApplyDefaults(config *ConfigArr, lastModified time.Time) {
	var ttl uint32
	var rtype uint16
	var class uint16

	if config.Defaults.TTL > 0 {
		ttl = config.Defaults.TTL
	} else {
		ttl = 3600
	}

	if config.Defaults.Type > 0 {
		rtype = config.Defaults.Type
	} else {
		rtype = 1
	}

	if config.Defaults.Class > 0 {
		class = config.Defaults.Class
	} else {
		class = 1
	}

	if !lastModified.IsZero() {
		config.Domain.Modified = lastModified
	}

	for i := 0; i < len(config.Records); i++ {
		if config.Records[i].TTL == 0 {
			config.Records[i].TTL = ttl
		}

		if config.Records[i].Type == 0 {
			config.Records[i].Type = rtype
		}

		if config.Records[i].Class == 0 {
			config.Records[i].Class = class
		}

		// Set default MX record preference if undefined
		if config.Records[i].Type == 15 && config.Records[i].Preference == 0 {
			config.Records[i].Preference = 10
		}

		// Append the root domain to the record
		config.Records[i].Domain = fmt.Sprintf("%s%s.", config.Records[i].Domain, config.Domain.Domain)

		// Check record size, 255 bytes max
		rsize := len(config.Records[i].Address)
		if rsize > 255 {
			config.Records[i].Address = config.Records[i].Address[:255]
			slog.Warn("record size too large, 255 byte limit, truncated", "domain", config.Records[i].Domain)
		}
	}
}

// ReadZoneFiles loads all zone files from zone_dir. For an s3:// zone_dir, s3cfg
// must be non-nil; otherwise it reads from the local filesystem.
func ReadZoneFiles(zone_dir string, s3cfg *S3Config) *Config {
	slog.Info("ReadZoneFiles: reading", "dir", zone_dir)

	t := &Config{}
	t.Domain = make(map[string]Domain, 4)
	t.Records = make(map[DomainLookup][]Records, 4)

	start := time.Now()

	if strings.HasPrefix(zone_dir, "s3://") {
		if s3cfg == nil {
			slog.Error("ReadZoneFiles: s3:// zone_dir requires S3 config", "zone_dir", zone_dir)
			return t
		}

		sess := newS3Session(s3cfg)
		svc := s3.New(sess)

		path := strings.Split(zone_dir, "s3://")

		if len(path) < 2 {
			slog.Error("ReadZoneFiles: invalid s3:// zone_dir", "zone_dir", zone_dir)
			return t
		}

		resp, err := svc.ListObjectsV2(&s3.ListObjectsV2Input{Bucket: aws.String(path[1])})
		if err != nil {
			slog.Error("unable to list items in bucket", "bucket", path[1], "error", err)
			return t
		}

		for _, item := range resp.Contents {
			if strings.HasSuffix(*item.Key, ".toml") {
				myconfig, err := ReadZone(fmt.Sprintf("%s/%s", zone_dir, *item.Key), *item.LastModified, s3cfg)

				if err == nil {
					err = checkConfigDomainMatch(*item.Key, myconfig.Domain.Domain)

					if err == nil {
						t.AddZone(myconfig)
					} else {
						slog.Error("unable to load item", "key", *item.Key, "error", err)
					}
				} else {
					slog.Error("unable to download item", "key", *item.Key, "error", err)
				}
			}
		}
	} else {
		files, err := os.ReadDir(zone_dir)

		if err != nil {
			slog.Error("failed reading directory", "error", err)
		}

		for _, file := range files {
			filename := fmt.Sprintf("%s/%s", zone_dir, file.Name())

			info, err := file.Info()
			if err != nil {
				slog.Warn("failed to get file info", "error", err)
				continue
			}

			myconfig, err := ReadZone(filename, info.ModTime(), nil)
			if err == nil {
				t.AddZone(myconfig)
			}
		}
	}

	elapsed := time.Since(start)
	slog.Info("config files read", "elapsed", elapsed)

	return t
}

func (t *Config) AddZone(myconfig ConfigArr) {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	for _, item := range myconfig.Records {
		record := DomainLookup{Domain: item.Domain, Type: item.Type, Class: item.Class}
		t.Records[record] = append(t.Records[record], item)
		myconfig.Domain.RecordRef = append(myconfig.Domain.RecordRef, record)
	}

	t.Domain[myconfig.Domain.Domain] = myconfig.Domain

	slog.Info("added zone to local DNS DB", "domain", myconfig.Domain.Domain)
}

func (t *Config) DeleteZone(domain string) {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	record, ok := t.Domain[domain]
	if !ok {
		return
	}

	for _, v := range record.RecordRef {
		delete(t.Records, v)
	}

	delete(t.Domain, domain)

	slog.Info("DeleteZone: removed zone from local DNS DB", "domain", domain)
}

// ReadZone parses a single zone file from the filesystem or S3. For an s3://
// zone_file, s3cfg must be non-nil.
func ReadZone(zone_file string, lastModified time.Time, s3cfg *S3Config) (myconfig ConfigArr, err error) {
	slog.Debug("ReadZone: parsing zone file", "file", zone_file, "modified", lastModified)

	if strings.HasPrefix(zone_file, "s3://") {
		if s3cfg == nil {
			return myconfig, errors.New("s3:// zone_file requires S3 config")
		}

		s3path := strings.Split(zone_file, "s3://")
		paths := strings.SplitN(s3path[1], "/", 2)

		if len(paths) < 2 {
			return myconfig, errors.New("path not found in S3")
		}

		sess := newS3Session(s3cfg)

		buff := &aws.WriteAtBuffer{}
		downloader := s3manager.NewDownloader(sess)

		numBytes, err := downloader.Download(buff,
			&s3.GetObjectInput{
				Bucket: aws.String(paths[0]),
				Key:    aws.String(paths[1]),
			})

		if err != nil {
			return myconfig, fmt.Errorf("download %s: %w", zone_file, err)
		}

		if numBytes == 0 {
			return myconfig, errors.New("config file empty")
		}

		if err := toml.Unmarshal(buff.Bytes(), &myconfig); err != nil {
			return myconfig, err
		}
		ApplyDefaults(&myconfig, lastModified)
	} else {
		file, err := os.ReadFile(zone_file)

		if err != nil {
			return myconfig, fmt.Errorf("error reading %s: %w", zone_file, err)
		}

		if err := toml.Unmarshal(file, &myconfig); err != nil {
			return myconfig, err
		}
		ApplyDefaults(&myconfig, lastModified)
	}

	return myconfig, nil
}

func checkConfigDomainMatch(filename string, domain string) (err error) {
	filecheck := strings.Replace(filepath.Base(filename), ".toml", "", 1)

	if filecheck != domain {
		err = fmt.Errorf("config file %s (%s) does not match domain entry %s", filename, filecheck, domain)
	}

	return err
}
