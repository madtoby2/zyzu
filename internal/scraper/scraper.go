package scraper

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/madtoby2/zyzu/internal/store"
)

const (
	baseURL     = "https://www.ziyuanzu.com"
	sitemapURL  = baseURL + "/sitemap-sources.xml"
	userAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
	requestDelay = 200 * time.Millisecond
)

type Scraper struct {
	client *http.Client
}

func New() *Scraper {
	return &Scraper{
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// ScrapeAll fetches all 77 stations with full detail.
func (s *Scraper) ScrapeAll() ([]store.Station, error) {
	slugs, err := s.getStationSlugs()
	if err != nil {
		return nil, fmt.Errorf("get slugs: %w", err)
	}

	var stations []store.Station
	for i, slug := range slugs {
		st, err := s.scrapeDetail(slug)
		if err != nil {
			fmt.Printf("  WARN: skip %s: %v\n", slug, err)
			continue
		}
		stations = append(stations, st)
		if (i+1)%20 == 0 {
			fmt.Printf("  scraped %d/%d\n", i+1, len(slugs))
		}
		time.Sleep(requestDelay)
	}
	return stations, nil
}

// getStationSlugs extracts all station page slugs from the sitemap.
func (s *Scraper) getStationSlugs() ([]string, error) {
	body, err := s.fetch(sitemapURL)
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile(`<loc>https://www\.ziyuanzu\.com/source/([a-zA-Z0-9]+-\d+)</loc>`)
	matches := re.FindAllStringSubmatch(body, -1)

	seen := map[string]bool{}
	var slugs []string
	for _, m := range matches {
		slug := m[1]
		if !seen[slug] {
			seen[slug] = true
			slugs = append(slugs, slug)
		}
	}
	return slugs, nil
}

// scrapeDetail fetches a station detail page and extracts all info.
func (s *Scraper) scrapeDetail(slug string) (store.Station, error) {
	url := baseURL + "/source/" + slug
	body, err := s.fetch(url)
	if err != nil {
		return store.Station{}, err
	}

	st := store.Station{Slug: slug}

	// Name from h1
	if m := regexp.MustCompile(`<h1[^>]*>([^<]+)</h1>`).FindStringSubmatch(body); len(m) > 1 {
		st.Name = strings.TrimSuffix(m[1], " - 采集接口与实时监控")
	}

	// Category from breadcrumb
	if m := regexp.MustCompile(`<a[^>]*href="/category/([^"]*)"[^>]*>([^<]+)</a>`).FindStringSubmatch(body); len(m) > 2 {
		st.Category = m[2]
	}

	// Tags
	tagRe := regexp.MustCompile(`<a[^>]*href="/tag/[^"]*"[^>]*>([^<]+)</a>`)
	tagMatches := tagRe.FindAllStringSubmatch(body, -1)
	var tags []string
	for _, tm := range tagMatches {
		t := tm[1]
		if t != "查看更多 →" {
			tags = append(tags, t)
		}
	}
	if len(tags) > 0 {
		tagJSON, _ := json.Marshal(tags)
		st.Tags = string(tagJSON)
	} else {
		st.Tags = "[]"
	}

	// API URL from input value
	apiRe := regexp.MustCompile(`value="(https?://[^"]*(?:api|provide|json)[^"]*)"`)
	apiMatches := apiRe.FindAllStringSubmatch(body, -1)
	for _, am := range apiMatches {
		if !strings.Contains(am[1], "example.com") {
			st.APIURL = am[1]
			break
		}
	}

	// Interface type
	if strings.Contains(body, "JSON接口") {
		st.InterfaceType = "JSON"
	}
	if strings.Contains(body, "XML接口") {
		if st.InterfaceType != "" {
			st.InterfaceType += "/XML"
		} else {
			st.InterfaceType = "XML"
		}
	}

	// Resource count
	if m := regexp.MustCompile(`(\d+\.?\d*万?)\s*条\s*资源`).FindStringSubmatch(body); len(m) > 1 {
		st.ResourceCount = m[1]
	}

	// Availability
	if m := regexp.MustCompile(`(\d+\.?\d*%)[^<]{0,50}实际可用率`).FindStringSubmatch(body); len(m) > 1 {
		st.Availability = m[1]
	}

	// Response time
	if m := regexp.MustCompile(`(\d+ms)[^<]{0,50}(?:稳定|极速|低延迟)`).FindStringSubmatch(body); len(m) > 1 {
		st.ResponseTime = m[1]
	}

	// Description
	if m := regexp.MustCompile(`<p[^>]*class="[^"]*text-gray-600[^"]*"[^>]*>([^<]+)</p>`).FindStringSubmatch(body); len(m) > 1 {
		st.Description = m[1]
	}

	return st, nil
}

func (s *Scraper) fetch(url string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xml,*/*")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2MB limit
	if err != nil {
		return "", err
	}
	return string(data), nil
}
