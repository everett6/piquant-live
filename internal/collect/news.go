package collect

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DefaultFeeds are free public RSS feeds (no API key) verified reachable on 2026-11-10:
// crypto micro news, plus macro/regulatory releases (Fed, SEC, CFTC).
var DefaultFeeds = []string{
	"https://cointelegraph.com/rss",
	"https://www.coindesk.com/arc/outboundfeeds/rss/",
	"https://decrypt.co/feed",
	"https://bitcoinmagazine.com/feed",
	"https://cryptoslate.com/feed/",
	"https://www.theblock.co/rss.xml",
	"https://news.bitcoin.com/feed/",
	"https://www.newsbtc.com/feed/",
	"https://www.federalreserve.gov/feeds/press_all.xml",
	"https://www.sec.gov/news/pressreleases.rss",
	"https://www.cftc.gov/RSS/RSSGP/rssgp.xml",
}

// NewsRow is one headline as first observed by the collector.
type NewsRow struct {
	ID          string    `json:"id"`
	ObservedAt  time.Time `json:"observed_at"`  // when we first saw it (use this for causal labels)
	PublishedAt time.Time `json:"published_at"` // feed-reported time (may be missing or wrong)
	Feed        string    `json:"feed"`
	Source      string    `json:"source"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary,omitempty"`
	URL         string    `json:"url,omitempty"`
}

type rssDoc struct {
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title       string `xml:"title"`
			Description string `xml:"description"`
			Link        string `xml:"link"`
			PubDate     string `xml:"pubDate"`
			GUID        string `xml:"guid"`
		} `xml:"item"`
	} `xml:"channel"`
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func cleanText(s string, max int) string {
	s = html.UnescapeString(tagRE.ReplaceAllString(s, " "))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func parseFeedTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339, "Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// headlineID is stable across polls: hash of the link (or title if no link).
func headlineID(link, title string) string {
	key := strings.TrimSpace(link)
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(title))
	}
	h := sha1.Sum([]byte(key))
	return hex.EncodeToString(h[:10])
}

// parseRSS converts an RSS document into rows observed at now.
func parseRSS(body []byte, feed string, now time.Time) ([]NewsRow, error) {
	var doc rssDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if len(doc.Channel.Items) == 0 {
		return nil, fmt.Errorf("no items")
	}
	rows := make([]NewsRow, 0, len(doc.Channel.Items))
	for _, it := range doc.Channel.Items {
		title := cleanText(it.Title, 400)
		if title == "" {
			continue
		}
		link := strings.TrimSpace(it.Link)
		if link == "" {
			link = strings.TrimSpace(it.GUID)
		}
		rows = append(rows, NewsRow{
			ID:          headlineID(link, title),
			ObservedAt:  now,
			PublishedAt: parseFeedTime(it.PubDate),
			Feed:        feed,
			Source:      cleanText(doc.Channel.Title, 120),
			Title:       title,
			Summary:     cleanText(it.Description, 1000),
			URL:         link,
		})
	}
	return rows, nil
}

func httpGet(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	// Generic client name; no personal contact details are sent to third parties.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; PiQuant-Research/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return body, resp.StatusCode, err
}

// newsTracker de-duplicates headlines across polls and restarts.
type newsTracker struct {
	seen map[string]time.Time
}

func newNewsTracker(newsDir string) *newsTracker {
	t := &newsTracker{seen: map[string]time.Time{}}
	for _, f := range recentDayFiles(newsDir, 7) {
		_ = ReadRows(f, func(line []byte) error {
			var r NewsRow
			if json.Unmarshal(line, &r) == nil && r.ID != "" {
				t.seen[r.ID] = r.ObservedAt
			}
			return nil
		})
	}
	return t
}

// fresh reports whether id is new and marks it seen.
func (t *newsTracker) fresh(id string, now time.Time) bool {
	if _, ok := t.seen[id]; ok {
		return false
	}
	t.seen[id] = now
	return true
}

func (t *newsTracker) prune(now time.Time) {
	for id, at := range t.seen {
		if now.Sub(at) > 8*24*time.Hour {
			delete(t.seen, id)
		}
	}
}
