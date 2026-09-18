package ziz

import (
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Term struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Taxonomy string `json:"taxonomy"`
	Link     string `json:"link,omitempty"`
}

type Author struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
	Link string `json:"link,omitempty"`
}

type FeaturedMedia struct {
	ID        int    `json:"id"`
	SourceURL string `json:"source_url,omitempty"`
	AltText   string `json:"alt_text,omitempty"`
	Caption   string `json:"caption,omitempty"`
}

type Article struct {
	Source          string         `json:"source"`
	SourceID        int            `json:"source_id"`
	LegacyURL       string         `json:"legacy_url"`
	Slug            string         `json:"slug"`
	Title           string         `json:"title"`
	ExcerptHTML     string         `json:"excerpt_html,omitempty"`
	ExcerptText     string         `json:"excerpt_text,omitempty"`
	BodyHTML        string         `json:"body_html"`
	BodyText        string         `json:"body_text"`
	PublishedAt     string         `json:"published_at"`
	ModifiedAt      string         `json:"modified_at,omitempty"`
	Author          *Author        `json:"author,omitempty"`
	Categories      []Term         `json:"categories,omitempty"`
	Tags            []Term         `json:"tags,omitempty"`
	FeaturedMedia   *FeaturedMedia `json:"featured_media,omitempty"`
	MediaURLs       []string       `json:"media_urls,omitempty"`
	ScrapedAt       string         `json:"scraped_at"`
}

type wpRendered struct {
	Rendered string `json:"rendered"`
}

type wpAuthor struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Link string `json:"link"`
}

type wpTerm struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Taxonomy string `json:"taxonomy"`
	Link     string `json:"link"`
}

type wpMedia struct {
	ID        int        `json:"id"`
	SourceURL string     `json:"source_url"`
	AltText   string     `json:"alt_text"`
	Caption   wpRendered `json:"caption"`
}

type wpEmbedded struct {
	Authors       []wpAuthor  `json:"author"`
	FeaturedMedia []wpMedia   `json:"wp:featuredmedia"`
	Terms         [][]wpTerm  `json:"wp:term"`
}

type wpPost struct {
	ID            int        `json:"id"`
	Date          string     `json:"date"`
	DateGMT       string     `json:"date_gmt"`
	Modified      string     `json:"modified"`
	ModifiedGMT   string     `json:"modified_gmt"`
	Slug          string     `json:"slug"`
	Link          string     `json:"link"`
	Title         wpRendered `json:"title"`
	Content       wpRendered `json:"content"`
	Excerpt       wpRendered `json:"excerpt"`
	AuthorID      int        `json:"author"`
	FeaturedID    int        `json:"featured_media"`
	CategoryIDs   []int      `json:"categories"`
	TagIDs        []int      `json:"tags"`
	Embedded      wpEmbedded `json:"_embedded"`
}

var (
	tagPattern   = regexp.MustCompile(`(?s)<[^>]*>`)
	spacePattern = regexp.MustCompile(`\s+`)
	attrPattern  = regexp.MustCompile(`(?i)(?:src|href|srcset)\s*=\s*["']([^"']+)["']`)
)

func cleanText(value string) string {
	if value == "" {
		return ""
	}
	value = tagPattern.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	value = spacePattern.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func parseWPTime(primary, fallback string) string {
	for _, raw := range []string{primary, fallback} {
		if raw == "" || raw == "0000-00-00T00:00:00" {
			continue
		}
		if t, err := time.Parse("2006-01-02T15:04:05", raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func articleFromWP(base *url.URL, post wpPost, scrapedAt time.Time) Article {
	article := Article{
		Source:      "zizonline",
		SourceID:    post.ID,
		LegacyURL:   post.Link,
		Slug:        post.Slug,
		Title:       cleanText(post.Title.Rendered),
		ExcerptHTML: post.Excerpt.Rendered,
		ExcerptText: cleanText(post.Excerpt.Rendered),
		BodyHTML:    post.Content.Rendered,
		BodyText:    cleanText(post.Content.Rendered),
		PublishedAt: parseWPTime(post.DateGMT, post.Date),
		ModifiedAt:  parseWPTime(post.ModifiedGMT, post.Modified),
		ScrapedAt:   scrapedAt.UTC().Format(time.RFC3339),
	}

	if len(post.Embedded.Authors) > 0 {
		a := post.Embedded.Authors[0]
		article.Author = &Author{ID: a.ID, Name: a.Name, Slug: a.Slug, Link: a.Link}
	}

	for _, group := range post.Embedded.Terms {
		for _, t := range group {
			term := Term{ID: t.ID, Name: cleanText(t.Name), Slug: t.Slug, Taxonomy: t.Taxonomy, Link: t.Link}
			switch t.Taxonomy {
			case "category":
				article.Categories = append(article.Categories, term)
			case "post_tag":
				article.Tags = append(article.Tags, term)
			}
		}
	}

	if len(post.Embedded.FeaturedMedia) > 0 {
		m := post.Embedded.FeaturedMedia[0]
		article.FeaturedMedia = &FeaturedMedia{
			ID:        m.ID,
			SourceURL: m.SourceURL,
			AltText:   m.AltText,
			Caption:   cleanText(m.Caption.Rendered),
		}
	}

	article.MediaURLs = extractMediaURLs(base, post.Content.Rendered)
	if article.FeaturedMedia != nil && article.FeaturedMedia.SourceURL != "" {
		article.MediaURLs = append(article.MediaURLs, article.FeaturedMedia.SourceURL)
	}
	article.MediaURLs = uniqueSorted(article.MediaURLs)
	return article
}

func extractMediaURLs(base *url.URL, body string) []string {
	var out []string
	for _, match := range attrPattern.FindAllStringSubmatch(body, -1) {
		if len(match) != 2 {
			continue
		}
		raw := html.UnescapeString(match[1])
		for _, candidate := range strings.Split(raw, ",") {
			candidate = strings.TrimSpace(candidate)
			if fields := strings.Fields(candidate); len(fields) > 0 {
				candidate = fields[0]
			}
			if candidate == "" || strings.HasPrefix(candidate, "data:") {
				continue
			}
			u, err := url.Parse(candidate)
			if err != nil {
				continue
			}
			if !u.IsAbs() {
				u = base.ResolveReference(u)
			}
			if !strings.Contains(u.Path, "/wp-content/uploads/") {
				continue
			}
			out = append(out, u.String())
		}
	}
	return out
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
