<h1 align=center>Dockette / Docshot</h1>

<p align=center>
   📸 One HTTP call turns any document, spreadsheet, deck, e-mail, archive, audio or video into a picture.
</p>

<p align=center>
🕹 <a href="https://f3l1x.io">f3l1x.io</a> | 💻 <a href="https://github.com/f3l1x">f3l1x</a> | 🐦 <a href="https://twitter.com/xf3l1x">@xf3l1x</a>
</p>

<p align=center>
  <a href="https://hub.docker.com/r/dockette/docshot/"><img src="https://badgen.net/docker/pulls/dockette/docshot"></a>
  <a href="https://bit.ly/ctteg"><img src="https://badgen.net/badge/support/gitter/cyan"></a>
  <a href="https://github.com/sponsors/f3l1x"><img src="https://badgen.net/badge/sponsor/donations/F96854"></a>
</p>

![](.docs/preview.png "docshot output: docx, pptx, xlsx, eml, markdown, zip")

-----

Upload a file and get back PNG, JPEG or WebP: page 1, page N, a contact sheet of many
pages, or a ZIP of every page. It handles 300+ file extensions: PDF, Office/OpenDocument,
HTML, Markdown, e-mail (eml/msg), EPUB, images (incl. HEIC/AVIF/RAW), video, audio,
archives, fonts, notebooks, CSV and more. It is self-hosted, runs in one container, and is
built for thumbnails, previews and feeding vision LLMs.

## Quick start

```sh
docker run -d -p 3000:3000 dockette/docshot
```

**Send a file, get a picture:**

```sh
curl -F file=@report.docx localhost:3000/upload       -o report.png   # first page
curl -F file=@deck.pptx   localhost:3000/upload/grid  -o deck.png     # contact sheet, up to 12 slides
curl -F file=@movie.mp4   localhost:3000/upload/grid  -o movie.png    # 3×3 frames with timestamps
curl -F file=@report.docx localhost:3000/upload/pages -o report.zip   # all pages
```

**Or just link to it, a plain `<img>` is enough:**

```html
<img src="http://localhost:3000/render?url=https://example.com/report.pdf&preset=thumb">
```

Remote URLs must match `DOCSHOT_FETCH_ALLOW` (e.g. `-e DOCSHOT_FETCH_ALLOW='^https://example\.com/'`).
Local files work with `?path=` and `DOCSHOT_ROOT`, S3 with `?s3=bucket/key`.

## What it renders

Every input is a **sequence of pages**. `page=N` picks one page, `/grid` lays several out,
and `/pages` zips them.

| Input | A page is | Engine |
|---|---|---|
| PDF; multipage TIFF; DjVu; XPS/OXPS | Page | PDFium (WebAssembly); tiff2pdf, ddjvu or xpstopdf first |
| doc docx odt rtf ppt pptx odp pages key vsd vsdx pub wpd cdr … (~100) | Page / slide | LibreOffice |
| xls xlsx ods numbers … | Sheet | LibreOffice, one page per sheet |
| csv tsv | Page of a table (header repeated) | Go → Chromium |
| html md epub; txt and 60 source/config types; json | Page (A4) | Chromium |
| eml mbox mht · **msg** (Outlook) | Page (A4) | MIME / MAPI → HTML → Chromium |
| ipynb · ics · vcf | Page: notebook with plots, agenda, contact cards | Go → Chromium |
| svg · Graphviz (dot, gv) | The drawing at its own size, vector | Chromium · Graphviz |
| png jpg gif webp bmp · heic avif jxl jp2 psd ico exr qoi tga … | The image (animated GIF/WebP: each frame) | Go · ffmpeg |
| Camera RAW: dng cr2 cr3 nef arw orf rw2 raf … | The embedded preview | LibRaw |
| Video: mp4 mov webm mkv avi … | A frame (`time=`); `/grid` spreads frames over the whole video | ffmpeg |
| Audio: mp3 wav flac ogg opus m4a … | A card: cover art, title/artist, duration, waveform | ffmpeg |
| Fonts: ttf otf woff woff2 | A specimen sheet | Chromium |
| Archives: zip tar 7z rar iso cab cbz cbr · gz xz bz2 zst | Each file inside, through its own engine | Go · libarchive |

`GET /formats` lists every extension. The `X-Render-Path` header says which engine drew
the picture.

## API

| Operation | Upload (`POST`, multipart `file` or raw body) | Link (`GET`, works in `<img>`) |
|---|---|---|
| One page | `/upload` | `/render?path=…` / `?s3=…` / `?url=…` |
| Contact sheet | `/upload/grid` | `/render/grid?…` |
| ZIP of all pages | `/upload/pages` | `/render/pages?…` |
| Metadata (JSON) | `/upload/info` | `/info?…` |

Send uploads as multipart `-F file=@…`, so the file name helps detection (csv, md, json,
source code). A raw body works too, named by `Content-Disposition` or `?name=`; binary
formats are detected from their content alone. Parameters go in the query string or as
extra form fields. An unknown parameter, or one
that belongs to another operation, is a `400` that says why and suggests the name you
probably meant.

| Parameter | Default | |
|---|---|---|
| `page` | `1` | Page, sheet, frame or archive member. For `/grid` and `/pages`, the first one. |
| `dpi` | `150` | 30–600 |
| `width`, `height` | – | Target box in px. Give one and the other follows the aspect ratio. Documents are rendered at that size, vector-sharp. |
| `fit` | `contain` | `contain`, `cover` (crop), `fill` (stretch), `pad` (exact box on a `bg` canvas) |
| `position` | `center` | What `cover` keeps, or where `pad` places the content: `top` (best for page thumbnails), `center`, `bottom` |
| `trim` | – | Cut uniform margins: `1`, or a threshold `2`–`100` |
| `pad` | `0` | Margin in px. Inside the box when there is one. |
| `bg` | `fff` | Background colour: `fff`, `f3f4f6`, `00000080`, `transparent` |
| `dpr` | `1` | Device pixel ratio, 1–4 (retina) |
| `format` | `png` | `png`, `jpeg`, `webp`, or `auto` (WebP when `Accept` allows it; `Vary: Accept`) |
| `quality` | `85` | JPEG and WebP |
| `time` | 10 % | **Page only.** Video frame: seconds or a clock (`42.5`, `1:23`, `1:02:03`) |
| `grid` | `auto` | **Grid only.** `COLSxROWS`, each 1–10. `auto` takes up to 12 items (video: 3×3). |
| `labels` | `1` | **Grid only.** Page numbers, timestamps or file names on the cells |
| `limit` | `20` | **Pages only.** Images in the ZIP, 1–200 |
| `preset` | – | `thumb` (300×400 WebP, top), `card` (480×270 WebP, top), `preview` (1200 px WebP), `og` (1200×630). Explicit parameters win. |
| `store` | – | `s3[:append\|replace\|suffix\|hash]`: keep the result in S3 (see below) |
| `fallback` | – | `1`: on error, a page icon with the file type (HTTP 200, error in `X-Docshot-Error`) |
| `download` | – | `1` or a file name: `Content-Disposition: attachment` |
| `name` | – | Input file name for raw-body uploads and URL sources, used for format detection |

### Response headers

| Header | |
|---|---|
| `X-Page-Count` | Pages, sheets, frames or archive members. A single video frame is 1. |
| `X-Source-Type` | The detected format |
| `X-Render-Path` | The engine that drew the picture |
| `X-Cache` | `miss`, `document`, `memory`, `shared` or `store` |
| `X-Effective-DPI` | DPI actually used, after the limits |
| `X-Duration` | Video or audio length in seconds |
| `X-Render-Ms` | Time spent on the server |
| `ETag`, `Cache-Control` | Successful answers only. Errors are `no-store`. |

### Errors

Errors are always JSON, from every endpoint, including unknown paths and wrong methods:

```json
{"error": "unknown parameter \"widht\" (did you mean \"width\"?)", "code": "unknown_parameter", "status": 400}
```

| `code` | Status | Meaning |
|---|---|---|
| `bad_request`, `unknown_parameter`, `parameter_not_applicable` | 400 | The request is wrong |
| `forbidden` | 403 | Bad signature, or a bucket or URL that is not allowed |
| `not_found` | 404 | Unknown path or source |
| `method_not_allowed` | 405 | Wrong HTTP method |
| `too_large` | 413 | Over the upload limit |
| `unsupported_format` | 415 | Nothing to render |
| `out_of_range` | 416 | `page` or `time` is past the end |
| `unreadable_file` | 422 | Corrupt file |
| `password_protected` | 422 | Encrypted PDF |
| `busy` | 429 | The queue is full |
| `engine_failed` | 502 | LibreOffice, Chromium or S3 failed |
| `unavailable` | 503 | Not ready |

### Previews in `<img>`: GET sources

```html
<img src="http://docshot:3000/render?path=contracts/2026/offer.docx&preset=card&format=auto&fallback=1">
```

| Source | Enable with |
|---|---|
| `?path=dir/file.docx` | `DOCSHOT_ROOT` + a mounted directory. Paths cannot escape it, not even through symlinks. |
| `?s3=bucket/key` | `DOCSHOT_S3_ENDPOINT` + keys, optionally `DOCSHOT_S3_BUCKETS` |
| `?url=https://…` | `DOCSHOT_FETCH_ALLOW=<regexp>`, checked on every redirect too |

### `?store=s3`: keep results

The result is written to S3, and the next identical request is served from there while
the source is unchanged:

| Strategy | `docs/a.docx` → page / grid / pages |
|---|---|
| `append` (default) | `a.docx.png` / `a.docx.grid.png` / `a.docx.pages.zip` |
| `replace` | `a.png` / `a.grid.png` / `a.pages.zip` |
| `suffix` | `a_preview.png` / `a_grid.png` / `a_pages.zip` |
| `hash` | `docshot/_hash/3f/3f9c….png`: the same content from any source maps to one object |

- **Variants:** non-default parameters add a variant to the name, e.g. `a.docx.p2.png` or
  `a.docx.p1-480x270-cover-top-q80.webp`.
- **Where:** an `?s3=` source is stored next to itself. Anything else goes to
  `DOCSHOT_STORE_BUCKET`.
- **Freshness:** the object carries `x-amz-meta-docshot-source` (the source's ETag or
  content hash) and is reused only while that still matches.

### Caching

| Layer | `X-Cache` | What it saves |
|---|---|---|
| Browser | – | `ETag` + `Cache-Control: max-age=3600` → `304` |
| S3 | `store` | The result in S3, see above |
| Memory | `memory` | The same image again (LRU, `DOCSHOT_CACHE_MB`) |
| Memory | `document` | The document's PDF: other pages and sizes skip LibreOffice (~15 ms instead of ~1 s) |
| In flight | `shared` | Identical concurrent requests wait for one conversion |

### Monitoring

| Endpoint | |
|---|---|
| `/healthz` | Liveness |
| `/readyz` | JSON readiness: Gotenberg and PDFium (503 when not ready) |
| `/metrics` | Prometheus: requests by cache layer, errors, conversions and renders with timings, queue, cache bytes, Gotenberg restarts, Go runtime |
| `/version` | JSON |
| `/formats` | Every extension and its engine |

## Configuration

| Env | Default | |
|---|---|---|
| `DOCSHOT_DEFAULT_DPI` | `150` | |
| `DOCSHOT_MAX_SIDE` / `DOCSHOT_MAX_PIXELS` / `DOCSHOT_MAX_BYTES` | `10000` / `40000000` / `104857600` | Longest edge, bitmap area, upload size |
| `DOCSHOT_TIMEOUT_SECONDS` | `180` | |
| `DOCSHOT_CONCURRENCY` / `DOCSHOT_QUEUE` | CPUs / `64` | Parallel work, and the waiting room before `429` |
| `DOCSHOT_CACHE_MB` / `DOCSHOT_MAX_AGE` | `256` / `3600` | |
| `DOCSHOT_ROOT` | – | Directory for `?path=` |
| `DOCSHOT_S3_ENDPOINT`, `_ACCESS_KEY`, `_SECRET_KEY`, `_REGION`, `_SSL`, `_BUCKETS` | – | `?s3=` and `?store=s3` |
| `DOCSHOT_STORE_BUCKET` / `_PREFIX` / `_STRATEGY` | – / `docshot/` / `append` | `?store=s3` |
| `DOCSHOT_FETCH_ALLOW` | – | Regexp of URLs `?url=` may fetch |
| `DOCSHOT_SIGN_KEY` | – | Require `?sign=` (HMAC-SHA256) on GET. Generate with `docshot -sign '/render?path=a.docx'`. |
| `DOCSHOT_PRESETS` / `DOCSHOT_ONLY_PRESETS` | – / `false` | `name:query;…` / allow presets only |
| `DOCSHOT_FALLBACK` | `false` | Placeholder instead of errors by default |
| `GOTENBERG_URL` | – | Use an external Gotenberg (run with no arguments, `CMD []`) |

## How it works

`tini` starts the `docshot` binary (Go, no CGO) on `:3000`, which supervises Gotenberg 8 on
`127.0.0.1:3001`. Gotenberg is internal only and its API is not exposed.

- **Documents** become PDF (LibreOffice, Chromium or a format-specific tool). PDFium,
  compiled to WebAssembly and running inside the Go binary, then draws the page at the
  requested size, so it stays vector-sharp.
- **Images, video and audio** skip the PDF step: Go and ffmpeg decode them, and libwebp
  (loaded through purego) encodes WebP.
- **Untrusted input:** the default `CMD` turns JavaScript off in Chromium and blocks the
  network for Chromium and LibreOffice. ffmpeg reads only local files.

## Caveats

- Fidelity is LibreOffice's, not Microsoft Office's. Metric-compatible fonts for Calibri,
  Cambria, Arial and Times are included.
- There is one LibreOffice per container, so office conversions queue. Scale out with more
  containers.
- There is no PostScript interpreter: EPS shows its bounding box, and `.ps` is a `415`.
- A format nothing can draw is a `415 unsupported_format`; there is no text-extraction fallback.
- There is no authentication. Anyone who reaches it can render, and with GET sources
  enabled also view every document under `DOCSHOT_ROOT` or in the buckets. Keep it on a
  private network, or set `DOCSHOT_SIGN_KEY` (with `DOCSHOT_ONLY_PRESETS` and `DOCSHOT_S3_BUCKETS`).
- `?store=s3` overwrites the computed key. `replace` and `suffix` can hit an unrelated
  `a.png` next to the source; use them only in buckets docshot owns.
- Archives render at most 200 members and skip nested archives and videos. A member that
  fails becomes a placeholder, so one broken file does not sink the grid.
- Source images over 3× `DOCSHOT_MAX_PIXELS` (120 MP) are a `422`, a decompression-bomb guard.

## Development

```
make build          # docker buildx build -t dockette/docshot:latest .
make up             # compose: build + start
make test-unit      # go vet + unit tests (Linux-only code, runs in a golang container)
make lint           # gofmt, staticcheck, govulncheck, shellcheck, hadolint
make smoke          # start the built image and render one page
make test           # API checks + fixtures, OCR-verified
make test-formats   # every format: generates/downloads ~230 files, renders and OCR-checks them
```

See [how to contribute](https://contributte.org/contributing.html) to this package.

This package is currently maintaining by these authors.

<a href="https://github.com/f3l1x">
    <img width="80" height="80" src="https://avatars2.githubusercontent.com/u/538058?v=3&s=80">
</a>

-----

Consider to [support](https://github.com/sponsors/f3l1x) **f3l1x**. Also thank you for using this package.
