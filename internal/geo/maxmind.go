package geo

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

const maxAge = 30 * 24 * time.Hour

func init() {
	providers["maxmind"] = newMaxmind
}

// downloadDB fetches url into dest atomically (tmp file + rename),
// extracting the .mmdb member from MaxMind's tar.gz. Package var so tests
// stub the network.
var downloadDB = func(rawURL, dest string) error {
	resp, err := http.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("geo: maxmind download HTTP %d", resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("geo: no .mmdb in archive")
		}
		if err != nil {
			return err
		}
		if strings.HasSuffix(hdr.Name, ".mmdb") {
			tmp := dest + ".tmp"
			f, err := os.Create(tmp)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
			return os.Rename(tmp, dest)
		}
	}
}

type maxmind struct {
	path   string
	mu     sync.RWMutex
	reader *maxminddb.Reader
	logger *slog.Logger
	stop   chan struct{}
}

func newMaxmind(u *url.URL, dataDir string, logger *slog.Logger) (Provider, error) {
	key := u.Host // maxmind://LICENSE_KEY
	if key == "" {
		return nil, fmt.Errorf("geo: maxmind DSN requires a license key (maxmind://KEY)")
	}
	m := &maxmind{path: filepath.Join(dataDir, "GeoLite2-Country.mmdb"), logger: logger, stop: make(chan struct{})}
	if err := m.ensureFresh(key); err != nil {
		return nil, err
	}
	r, err := maxminddb.Open(m.path)
	if err != nil {
		return nil, fmt.Errorf("geo: open mmdb: %w", err)
	}
	m.reader = r
	go m.refreshLoop(key)
	return m, nil
}

func (m *maxmind) ensureFresh(key string) error {
	st, err := os.Stat(m.path)
	if err == nil && time.Since(st.ModTime()) < maxAge {
		return nil
	}
	dl := fmt.Sprintf("https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country&license_key=%s&suffix=tar.gz", url.QueryEscape(key))
	if derr := downloadDB(dl, m.path); derr != nil {
		if err == nil {
			m.logger.Warn("geo: refresh failed, keeping stale db", "error", derr)
			return nil // stale but usable
		}
		return fmt.Errorf("geo: initial GeoLite2 download failed: %w", derr)
	}
	return nil
}

func (m *maxmind) refreshLoop(key string) {
	t := time.NewTicker(7 * 24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			if err := m.ensureFresh(key); err != nil {
				m.logger.Warn("geo: weekly refresh failed", "error", err)
				continue
			}
			if r, err := maxminddb.Open(m.path); err == nil {
				m.mu.Lock()
				old := m.reader
				m.reader = r
				m.mu.Unlock()
				old.Close()
			}
		}
	}
}

func (m *maxmind) Country(_ *http.Request, ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	var rec struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	m.mu.RLock()
	r := m.reader
	m.mu.RUnlock()
	if err := r.Lookup(addr).Decode(&rec); err != nil {
		return ""
	}
	return rec.Country.ISOCode
}

func (m *maxmind) Close() error {
	close(m.stop)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.reader != nil {
		return m.reader.Close()
	}
	return nil
}
