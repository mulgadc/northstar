package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	log "github.com/sirupsen/logrus"
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

func init() {
	_, logignore := os.LookupEnv("NORTHSTAR_LOG_IGNORE")

	if logignore {
		log.SetLevel(log.FatalLevel)
	}

	_, logdebug := os.LookupEnv("NORTHSTAR_LOG_DEBUG")

	if logdebug {
		log.SetLevel(log.DebugLevel)
	}
}

// newS3Session creates an AWS session with optional custom endpoint support
// for S3-compatible backends like Predastore.
func newS3Session() *session.Session {
	cfg := aws.Config{}

	if region := os.Getenv("AWS_REGION"); region != "" {
		cfg.Region = aws.String(region)
	}

	// Support custom S3-compatible endpoints (e.g., Predastore)
	if endpoint := os.Getenv("NORTHSTAR_S3_ENDPOINT"); endpoint != "" {
		cfg.Endpoint = aws.String(endpoint)
		cfg.S3ForcePathStyle = aws.Bool(true)
	}

	// Support explicit credentials via env vars
	accessKey := os.Getenv("AWS_ACCESS_KEY")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if accessKey != "" && secretKey != "" {
		cfg.Credentials = credentials.NewStaticCredentials(accessKey, secretKey, "")
	}

	// Skip TLS verification for self-signed certs (e.g., local Predastore)
	if os.Getenv("NORTHSTAR_S3_INSECURE") != "" {
		cfg.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	}

	return session.Must(session.NewSession(&cfg))
}

// FindZone walks up the domain labels to find which zone we are authoritative for.
// Returns the zone name and true if found, or empty string and false.
func (c *Config) FindZone(name string) (string, bool) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	// Walk up labels: "foo.bar.example.com." → "bar.example.com." → "example.com."
	labels := strings.Split(name, ".")
	for i := 0; i < len(labels); i++ {
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

func GenerateTestDomains(num int) (t Config) {
	t.Records = make(map[DomainLookup][]Records, 1)
	t.Domain = make(map[string]Domain, 1)

	for i := 0; i < num; i++ {
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

func (config *Config) MonitorConfig(zone_dir string) {
	var s3retry = os.Getenv("S3_SYNC_RETRY")

	if s3retry == "" {
		s3retry = "60"
	}

	s3retrysecs, _ := strconv.Atoi(s3retry)

	if strings.HasPrefix(zone_dir, "s3://") {
		go func() {
			sess := newS3Session()
			svc := s3.New(sess)

			for {
				time.Sleep(time.Second * time.Duration(s3retrysecs))

				log.Info("MonitorConfig: S3 check sync state")

				path := strings.Split(zone_dir, "s3://")

				if len(path) == 0 {
					log.Fatal("S3_BUCKET field required")
				}

				resp, err := svc.ListObjectsV2(&s3.ListObjectsV2Input{Bucket: aws.String(path[1])})

				if err != nil {
					log.Warnf("Unable to list items in bucket %q, %v", path, err)
					continue
				}

				configsync := make(map[string]bool, 10)

				for _, item := range resp.Contents {
					log.Debugf("MonitorConfig: Scanning %s", *item.Key)

					if strings.HasSuffix(*item.Key, ".toml") {
						domain := strings.Replace(*item.Key, ".toml", "", 1)
						configsync[domain] = true

						config.Mu.RLock()
						_, ok := config.Domain[domain]
						config.Mu.RUnlock()

						if !ok {
							myconfig, err := ReadZone(fmt.Sprintf("%s/%s", zone_dir, *item.Key), *item.LastModified)

							if err != nil {
								log.Warn(err)
								continue
							}

							err = checkConfigDomainMatch(*item.Key, myconfig.Domain.Domain)

							if err == nil {
								config.AddZone(myconfig)
							} else {
								log.Errorf("Domain %s and config file (%s) mismatch, entry skipped. %s", domain, *item.Key, err)
							}
						}

						config.Mu.RLock()
						domainEntry, exists := config.Domain[domain]
						config.Mu.RUnlock()

						if exists && *item.LastModified != domainEntry.Modified {
							log.Infof("MonitorConfig: New config file detected (%s), reloading", *item.Key)

							myconfig, err := ReadZone(fmt.Sprintf("%s/%s", zone_dir, *item.Key), *item.LastModified)

							if err != nil {
								log.Warn(err)
								continue
							}

							err = checkConfigDomainMatch(*item.Key, myconfig.Domain.Domain)

							if err == nil {
								config.DeleteZone(domainEntry.Domain)
								config.AddZone(myconfig)
							} else {
								log.Errorf("Domain %s and config file (%s) mismatch, entry skipped. %s", domain, *item.Key, err)
							}
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
					log.Debugf("MonitorConfig: Delete Check (%s)", domain)
					config.DeleteZone(domain)
				}
			}
		}()

	} else {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			log.Fatal(err)
		}
		defer watcher.Close()

		done := make(chan bool)
		go func() {
			for {
				select {
				case event, ok := <-watcher.Events:
					if !ok {
						return
					}
					log.Println("event:", event)

					if event.Op&fsnotify.Write == fsnotify.Write {
						log.Println("modified file:", event.Name)

						myconfig, err := ReadZone(event.Name, time.Now())
						if err != nil {
							log.Warn(err)
							continue
						}

						err = checkConfigDomainMatch(event.Name, myconfig.Domain.Domain)
						if err == nil {
							config.DeleteZone(myconfig.Domain.Domain)
							config.AddZone(myconfig)
						} else {
							log.Error(err)
						}
					}

					if event.Op&fsnotify.Create == fsnotify.Create {
						log.Println("new file:", event.Name)

						myconfig, err := ReadZone(event.Name, time.Now())
						if err != nil {
							log.Warn(err)
							continue
						}

						err = checkConfigDomainMatch(event.Name, myconfig.Domain.Domain)
						if err == nil {
							config.DeleteZone(myconfig.Domain.Domain)
							config.AddZone(myconfig)
						} else {
							log.Error(err)
						}
					}

					if event.Op&fsnotify.Remove == fsnotify.Remove {
						log.Println("remove file:", event.Name)

						domain := filepath.Base(event.Name)
						domain = strings.Replace(domain, ".toml", "", 1)

						config.DeleteZone(domain)
					}

				case err, ok := <-watcher.Errors:
					if !ok {
						return
					}
					log.Println("error:", err)
				}
			}
		}()

		err = watcher.Add(zone_dir)
		if err != nil {
			log.Fatal(err)
		}

		<-done
	}
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
			log.Warn(config.Records[i].Domain, " => Record size too large, 255 byte limit, truncated.")
		}
	}
}

func ReadZoneFiles(zone_dir string) (t Config) {
	log.Infof("ReadZoneFiles: Reading %s", zone_dir)

	t.Domain = make(map[string]Domain, 4)
	t.Records = make(map[DomainLookup][]Records, 4)

	start := time.Now()

	if strings.HasPrefix(zone_dir, "s3://") {
		sess := newS3Session()
		svc := s3.New(sess)

		path := strings.Split(zone_dir, "s3://")

		if len(path) == 0 {
			log.Fatal("S3_BUCKET field required")
		}

		resp, err := svc.ListObjectsV2(&s3.ListObjectsV2Input{Bucket: aws.String(path[1])})
		if err != nil {
			log.Errorf("Unable to list items in bucket %q, %v", path, err)
			return
		}

		for _, item := range resp.Contents {
			if strings.HasSuffix(*item.Key, ".toml") {
				myconfig, err := ReadZone(fmt.Sprintf("%s/%s", zone_dir, *item.Key), *item.LastModified)

				if err == nil {
					err = checkConfigDomainMatch(*item.Key, myconfig.Domain.Domain)

					if err == nil {
						t.AddZone(myconfig)
					} else {
						log.Errorf("Unable to load item %q, %v", item, err)
					}
				} else {
					log.Errorf("Unable to download item %q, %v", item, err)
				}
			}
		}
	} else {
		files, err := os.ReadDir(zone_dir)

		if err != nil {
			log.Errorf("failed reading directory: %s", err)
		}

		for _, file := range files {
			filename := fmt.Sprintf("%s/%s", zone_dir, file.Name())

			info, err := file.Info()
			if err != nil {
				log.Warnf("failed to get file info: %s", err)
				continue
			}

			myconfig, err := ReadZone(filename, info.ModTime())
			if err == nil {
				t.AddZone(myconfig)
			}
		}
	}

	elapsed := time.Since(start)
	log.Infof("Config files read in (%s)", elapsed)

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

	log.Infof("Added (%s) to local DNS zone DB", myconfig.Domain.Domain)
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

	log.Infof("DeleteZone: Removed (%s) from local DNS zone DB", domain)
}

func ReadZone(zone_file string, lastModified time.Time) (myconfig ConfigArr, err error) {
	log.Infof("ReadZone: Parsing Zone file (%s) (%s)", zone_file, lastModified)

	if strings.HasPrefix(zone_file, "s3://") {
		s3path := strings.SplitN(zone_file, "s3://", -1)
		paths := strings.SplitN(s3path[1], "/", 2)

		if len(paths) < 2 {
			return myconfig, errors.New("Path not found in S3")
		}

		sess := newS3Session()

		buff := &aws.WriteAtBuffer{}
		downloader := s3manager.NewDownloader(sess)

		numBytes, _ := downloader.Download(buff,
			&s3.GetObjectInput{
				Bucket: aws.String(paths[0]),
				Key:    aws.String(paths[1]),
			})

		if numBytes > 0 {
			toml.Unmarshal(buff.Bytes(), &myconfig)
			ApplyDefaults(&myconfig, lastModified)
		} else {
			return myconfig, errors.New("Config file empty")
		}
	} else {
		file, err := os.ReadFile(zone_file)

		if err != nil {
			errorMsg := fmt.Sprintf("Error reading %s %s", zone_file, err)
			log.Warn(errorMsg)
			return myconfig, errors.New(errorMsg)
		}

		toml.Unmarshal(file, &myconfig)
		ApplyDefaults(&myconfig, lastModified)
	}

	return
}

func checkConfigDomainMatch(filename string, domain string) (err error) {
	filecheck := strings.Replace(filepath.Base(filename), ".toml", "", 1)

	if filecheck != domain {
		err = fmt.Errorf("Config file %s (%s) does not match domain entry %s", filename, filecheck, domain)
	}

	return
}
