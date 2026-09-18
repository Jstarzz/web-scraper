# ZIZ public migration

`cmd/ziz-migrate` exports public ZIZ WordPress articles into a migration-friendly dataset and optionally downloads public media hosted under `/wp-content/uploads/`.

It intentionally uses the WordPress REST API before considering page crawling. With `per_page=100`, a ~34k article corpus is only a few hundred article API requests instead of tens of thousands of HTML page requests.

## Run

Fast first pass including media:

```bash
go run ./cmd/ziz-migrate \
  -base https://zizonline.com \
  -out /srv/ziz-migration \
  -workers 16 \
  -media-workers 24 \
  -media=true
```

Article-only pass:

```bash
go run ./cmd/ziz-migrate \
  -out /srv/ziz-migration \
  -workers 16 \
  -media=false
```

The default `-resume=true` skips article chunks and successful media already present. If a run is interrupted, run the same command again.

Do not raise concurrency blindly. The REST path already reduces request volume by roughly two orders of magnitude. If ZIZ responds with HTTP 429 or 5xx, the client retries with bounded backoff.

## Output

```text
/srv/ziz-migration/
├── articles/
│   ├── part-00001.jsonl.gz
│   ├── part-00002.jsonl.gz
│   └── ...
├── media/
│   ├── files/
│   │   ├── 00/
│   │   ├── 01/
│   │   └── ...
│   └── manifest.jsonl
└── summary.json
```

Each article chunk contains at most 100 newline-delimited JSON objects compressed with gzip level 1. Chunking lets REST pages download and write independently, removes a global output lock, and makes resume cheap.

Each article includes:

- WordPress source ID
- legacy URL and slug
- title
- excerpt HTML and text
- body HTML and text
- published and modified timestamps
- author
- categories and tags
- featured media metadata
- ZIZ-hosted media URLs found in the article
- scrape timestamp

Media files are content-addressed by SHA-256:

```text
media/files/ab/abcdef...jpg
```

Different URLs serving identical bytes therefore share one stored file. `media/manifest.jsonl` maps every original URL to its local path, hash, size and content type.

## Migration/import strategy

Use the article JSONL as the canonical migration dataset. The new website importer should map:

```text
source_id       -> legacy/source ID
legacy_url      -> redirects table
slug            -> article slug
title           -> title
body_html       -> article body
published_at    -> original publication time
modified_at     -> original modification time
author          -> author relation
categories/tags -> taxonomy relations
featured_media  -> media relation
```

After import, create permanent redirects from every `legacy_url` to the new article URL.

## Validation

At the end of a run, `summary.json` reports the WordPress article total seen during discovery, the number of unique exported articles, duplicates, empty bodies/titles, media counts, failures and timings.

If the exported unique count is below the WordPress total, rerun the command with `-resume=true`. Missing page chunks will be fetched without redoing successful chunks.
