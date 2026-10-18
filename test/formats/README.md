# Format matrix

Renders one or more files per supported extension (227 files) through `POST /upload` and OCR-checks the
result with a Tika server. A page passes when its `SENTINEL-…` token is readable, or,
for files without one, when OCR finds some text.

```bash
make test-formats   # needs a running docshot (:9994) and Tika (:9998); writes to $OUT (default /tmp/docshot-formats)
```

| File | Role |
|---|---|
| `gen.sh` | Runs inside the docshot image, with no network. Converts the fixtures to every LibreOffice and ffmpeg format. |
| `fixtures.py` | Hand-written fixtures: text and source files, web, mail, CSV, JSON, notebook, calendar, contacts, Graphviz, SVG, XPS and compressed files. It also decodes the `.uu` samples. |
| `corpus.txt` | Samples for formats nothing in the image can write: LibreOffice's import-filter corpus, Outlook msg, RAR, HEIC, PSD, NEF and woff. |
| `run.py` | Sends every file and prints one line per file with status, route, pages, time and OCR result. |
