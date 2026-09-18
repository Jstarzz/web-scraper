package ziz

import (
	"net/url"
	"testing"
	"time"
)

func TestExtractMediaURLs(t *testing.T) {
	base, err := url.Parse("https://zizonline.com/")
	if err != nil {
		t.Fatal(err)
	}

	body := `
		<p><img src="/wp-content/uploads/2026/09/photo.jpg"></p>
		<img srcset="https://zizonline.com/wp-content/uploads/2026/09/photo-300x200.jpg 300w, https://zizonline.com/wp-content/uploads/2026/09/photo.jpg 1200w">
		<a href="https://example.com/not-ziz.pdf">external</a>
	`

	got := extractMediaURLs(base, body)
	if len(got) != 2 {
		t.Fatalf("expected 2 unique media URLs, got %d: %#v", len(got), got)
	}
}

func TestArticleFromWP(t *testing.T) {
	base, _ := url.Parse("https://zizonline.com/")
	post := wpPost{
		ID:          123,
		DateGMT:     "2026-09-18T12:34:56",
		ModifiedGMT: "2026-09-18T13:00:00",
		Slug:        "hello-world",
		Link:        "https://zizonline.com/hello-world/",
		Title:       wpRendered{Rendered: "Hello &amp; World"},
		Excerpt:     wpRendered{Rendered: "<p>Short <strong>summary</strong>.</p>"},
		Content:     wpRendered{Rendered: "<p>Body <em>text</em>.</p><img src=\"/wp-content/uploads/2026/09/a.jpg\">"},
		Embedded: wpEmbedded{
			Authors: []wpAuthor{{ID: 7, Name: "ZIZ News", Slug: "ziz-news"}},
			Terms: [][]wpTerm{
				{{ID: 1, Name: "National", Slug: "national", Taxonomy: "category"}},
				{{ID: 2, Name: "Test", Slug: "test", Taxonomy: "post_tag"}},
			},
			FeaturedMedia: []wpMedia{{ID: 9, SourceURL: "https://zizonline.com/wp-content/uploads/2026/09/hero.jpg"}},
		},
	}

	article := articleFromWP(base, post, time.Unix(0, 0))
	if article.SourceID != 123 {
		t.Fatalf("source id: got %d", article.SourceID)
	}
	if article.Title != "Hello & World" {
		t.Fatalf("title: %q", article.Title)
	}
	if article.BodyText != "Body text." {
		t.Fatalf("body text: %q", article.BodyText)
	}
	if article.Author == nil || article.Author.Name != "ZIZ News" {
		t.Fatalf("author: %#v", article.Author)
	}
	if len(article.Categories) != 1 || article.Categories[0].Slug != "national" {
		t.Fatalf("categories: %#v", article.Categories)
	}
	if len(article.Tags) != 1 || article.Tags[0].Slug != "test" {
		t.Fatalf("tags: %#v", article.Tags)
	}
	if len(article.MediaURLs) != 2 {
		t.Fatalf("media URLs: %#v", article.MediaURLs)
	}
}
