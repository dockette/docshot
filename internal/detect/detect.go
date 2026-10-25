// Package detect decides which rendering route a document takes.
package detect

import (
	"archive/zip"
	"bytes"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/richardlehane/mscfb"
)

type route string

const (
	PDF      route = "pdf"   // already a PDF: rasterise directly
	Image    route = "image" // raster image Go can decode: re-encode, no PDF hop
	Office   route = "libreoffice"
	Sheet    route = "libreoffice-sheet" // same, but one page per sheet
	HTML     route = "chromium-html"
	Markdown route = "chromium-markdown"
	Email    route = "email"        // RFC 822 → self-contained HTML → Chromium
	EPUB     route = "epub"         // spine XHTML flattened → Chromium
	Text     route = "text"         // plain text / source code → <pre> → Chromium
	Video    route = "video"        // ffmpeg frames
	Archive  route = "archive"      // zip/tar: previews of the files inside
	TIFF     route = "tiff"         // tiff2pdf: every page of a multipage TIFF
	DjVu     route = "djvu"         // ddjvu → PDF
	XPS      route = "xps"          // xpstopdf → PDF
	Graphviz route = "graphviz"     // dot -Tpdf
	SVG      route = "chromium-svg" // a page of the SVG's own size → vector PDF
	Table    route = "table"        // CSV/TSV → HTML table
	JSON     route = "json"         // pretty-printed listing
	Notebook route = "notebook"     // Jupyter .ipynb → HTML
	Calendar route = "calendar"     // .ics → agenda
	Contacts route = "contacts"     // .vcf → cards
	Font     route = "font"         // specimen sheet
	Msg      route = "msg"          // Outlook .msg → mail page
	Audio    route = "audio"        // card: cover, tags, waveform (ffmpeg)
	Raw      route = "raw"          // camera RAW: embedded preview (LibRaw)
	Unknown  route = "unknown"      // nothing can render it: 415
)

type Kind struct {
	Ext   string // canonical extension without the dot, e.g. "docx"
	Route route
	Name  string // the file name, for formats that show it (fonts, audio)
}

// LibreOffice formats Gotenberg accepts.
var officeExts = set(
	"123", "602", "abw", "bib", "cdr", "cgm", "cmx", "cwk", "dbf", "dif", "doc", "docm", "docx", "dot", "dotm", "dotx",
	"dxf", "emf", "eps", "fodg", "fodp", "fodt", "hwp", "key", "lwp", "mcw", "met", "mw",
	"odd", "odg", "odm", "odp", "odt", "otg", "oth", "otp", "ott", "pages", "pcd", "pct", "pdb",
	"pot", "potm", "potx", "pps", "ppt", "pptm", "pptx", "psw", "pub", "pwp", "rtf",
	"sda", "sdd", "sdp", "sdw", "sgl", "smf", "std", "sti", "stw", "svm", "sxd", "sxg", "sxi", "sxm",
	"sxw", "vdx", "vor", "vsd", "vsdm", "vsdx", "wmf", "wpd", "wpg", "wps", "zabw",
	"pmd", "fb2", "lrf", "ppsx", "ppsm", "vstx", "vssx",
)

var sheetExts = set(
	"fods", "numbers", "ods", "ots", "pxl", "sdc", "slk", "stc", "sxc", "wb2", "wk1", "wks",
	"xls", "xlsb", "xlsm", "xlsx", "xlt", "xltm", "xltx", "xlw",
)

// Go decodes png/jpeg/gif/bmp/tiff/webp itself; the rest goes through ffmpeg.
var imageExts = set("png", "jpg", "jpeg", "jfif", "gif", "bmp", "webp",
	"heic", "heif", "avif", "jxl", "jp2", "j2k", "jpx", "jpf", "psd", "ico", "cur", "dds", "exr", "hdr", "qoi",
	"pcx", "pbm", "pgm", "ppm", "pnm", "pam", "pfm", "tga", "xbm", "xpm", "ras", "sun", "sgi", "rgb", "xwd")

var VideoExts = set("mp4", "m4v", "mov", "qt", "webm", "mkv", "avi", "wmv", "flv", "mpg", "mpeg", "m2ts", "mts", "ts", "3gp", "ogv")

// zip/tar/tar.gz natively, the rest via bsdtar; single compressed files become a one-member archive.
var ArchiveExts = set("zip", "tar", "tgz", "jar", "cbz", "cbt",
	"7z", "cb7", "rar", "cbr", "iso", "cab", "txz", "tbz", "tbz2", "tzst", "tlz",
	"gz", "xz", "bz2", "zst", "lz4", "lzma", "lz", "z")

var CompressedExts = set("gz", "xz", "bz2", "zst", "lz4", "lzma", "lz", "z")

var audioExts = set("mp3", "wav", "flac", "ogg", "oga", "opus", "m4a", "aac", "wma", "aif", "aiff", "amr", "ape", "mka", "wv", "mid", "midi")

var rawExts = set("dng", "cr2", "cr3", "crw", "nef", "nrw", "arw", "srf", "sr2", "orf", "rw2", "raf", "pef", "srw", "x3f", "3fr", "erf", "kdc", "dcr", "mrw", "iiq", "rwl")

var fontExts = set("ttf", "otf", "woff", "woff2", "ttc")

var htmlExts = set("html", "htm", "xhtml")

var markdownExts = set("md", "markdown", "mdown", "mkd")

var emailExts = set("eml", "mbox", "mht", "mhtml")

var textExts = set(
	"txt", "text", "log", "jsonl", "ndjson", "xml", "yaml", "yml", "toml", "ini", "cfg", "conf", "env",
	"properties", "srt", "vtt", "tex", "rst", "adoc", "org", "diff", "patch",
	"py", "java", "go", "rs", "c", "h", "cc", "cpp", "hpp", "cs", "js", "mjs", "ts", "tsx", "jsx", "rb", "php",
	"pl", "lua", "kt", "kts", "swift", "scala", "sh", "bash", "zsh", "ps1", "bat", "sql", "css", "scss", "less",
	"r", "m", "dart", "groovy", "gradle", "dockerfile", "makefile", "vue", "svelte",
)

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func routeForExt(ext string) (route, bool) {
	switch {
	case ext == "pdf":
		return PDF, true
	case ext == "tif" || ext == "tiff":
		return TIFF, true
	case ext == "djvu" || ext == "djv":
		return DjVu, true
	case ext == "xps" || ext == "oxps":
		return XPS, true
	case ext == "gv":
		return Graphviz, true
	case ext == "svg":
		return SVG, true
	case ext == "csv" || ext == "tsv" || ext == "tab":
		return Table, true
	case ext == "json" || ext == "geojson":
		return JSON, true
	case ext == "ipynb":
		return Notebook, true
	case ext == "ics" || ext == "ifb":
		return Calendar, true
	case ext == "vcf" || ext == "vcard":
		return Contacts, true
	case ext == "msg":
		return Msg, true
	case fontExts[ext]:
		return Font, true
	case audioExts[ext]:
		return Audio, true
	case rawExts[ext]:
		return Raw, true
	case imageExts[ext]:
		return Image, true
	case VideoExts[ext]:
		return Video, true
	case ArchiveExts[ext]:
		return Archive, true
	case sheetExts[ext]:
		return Sheet, true
	case officeExts[ext]:
		return Office, true
	case htmlExts[ext]:
		return HTML, true
	case markdownExts[ext]:
		return Markdown, true
	case emailExts[ext]:
		return Email, true
	case ext == "epub":
		return EPUB, true
	case textExts[ext]:
		return Text, true
	}
	return "", false
}

// Detect trusts a known extension, else sniffs the bytes.
func Detect(name string, body []byte) Kind {
	k := detectKind(name, body)
	k.Name = name
	return k
}

func detectKind(name string, body []byte) Kind {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	for suffix, short := range map[string]string{".tar.gz": "tgz", ".tar.xz": "txz", ".tar.bz2": "tbz2", ".tar.zst": "tzst", ".tar.lz": "tlz"} {
		if strings.HasSuffix(strings.ToLower(name), suffix) {
			ext = short
		}
	}
	// .dot is both a Word 97 template (OLE2) and a Graphviz graph (text).
	if ext == "dot" && isGraphviz(body) {
		return Kind{Ext: "dot", Route: Graphviz}
	}
	if base := strings.ToLower(name); base == "dockerfile" || base == "makefile" {
		ext = base
	}
	if r, ok := routeForExt(ext); ok {
		// An image behind the wrong extension (image.php, photo.pdf) is still an image.
		// RAW files are TIFF-shaped, so they keep their extension.
		if r != Raw {
			if ext2 := sniffImage(body); ext2 != "" {
				if r2, ok := routeForExt(ext2); ok && r2 != r {
					return Kind{Ext: ext2, Route: r2}
				}
			}
		}
		return Kind{Ext: ext, Route: r}
	}
	if ext2 := sniff(body); ext2 != "" {
		if r, ok := routeForExt(ext2); ok {
			return Kind{Ext: ext2, Route: r}
		}
		return Kind{Ext: ext2, Route: Unknown} // e.g. PostScript: text, but not to be shown as a listing
	}
	if utf8.Valid(head(body, 64<<10)) && !bytes.Contains(head(body, 64<<10), []byte{0}) {
		return Kind{Ext: "txt", Route: Text}
	}
	if ext == "" {
		ext = "bin"
	}
	return Kind{Ext: ext, Route: Unknown}
}

func head(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}

// sniffImage recognises raster images by magic bytes; "" when it is none.
func sniffImage(b []byte) string {
	h := head(b, 512)
	switch {
	case bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(h, []byte{0xff, 0xd8, 0xff}):
		return "jpg"
	case bytes.HasPrefix(h, []byte("GIF87a")), bytes.HasPrefix(h, []byte("GIF89a")):
		return "gif"
	case isBMP(h):
		return "bmp"
	case bytes.HasPrefix(h, []byte("II*\x00")), bytes.HasPrefix(h, []byte("MM\x00*")):
		return "tiff"
	case len(h) > 12 && bytes.HasPrefix(h, []byte("RIFF")) && string(h[8:12]) == "WEBP":
		return "webp"
	}
	return ""
}

// isBMP checks the file header's reserved field and the DIB header size, so
// text starting with "BM" is not taken for a bitmap.
func isBMP(h []byte) bool {
	if len(h) < 18 || !bytes.HasPrefix(h, []byte("BM")) || h[6] != 0 || h[7] != 0 || h[8] != 0 || h[9] != 0 {
		return false
	}
	switch h[14] {
	case 12, 40, 52, 56, 64, 108, 124:
		return h[15] == 0 && h[16] == 0 && h[17] == 0
	}
	return false
}

// sniff recognises formats by magic bytes; "" when unsure.
func sniff(b []byte) string {
	h := head(b, 512)
	if ext := sniffImage(b); ext != "" {
		return ext
	}
	switch {
	case bytes.HasPrefix(h, []byte("%PDF-")):
		return "pdf"
	case bytes.HasPrefix(h, []byte("%!PS")):
		if line, _, _ := bytes.Cut(h, []byte("\n")); bytes.Contains(line, []byte("EPSF")) {
			return "eps"
		}
		return "ps"
	case bytes.HasPrefix(h, []byte(`{\rtf`)):
		return "rtf"
	case bytes.HasPrefix(h, []byte("AT&TFORM")):
		return "djvu"
	case bytes.HasPrefix(h, []byte("7z\xbc\xaf\x27\x1c")):
		return "7z"
	case bytes.HasPrefix(h, []byte("Rar!\x1a\x07")):
		return "rar"
	case bytes.HasPrefix(h, []byte("\xfd7zXZ\x00")):
		return "xz"
	case bytes.HasPrefix(h, []byte("BZh")) && len(h) > 4 && h[3] >= '1' && h[3] <= '9':
		return "bz2"
	case bytes.HasPrefix(h, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return "zst"
	case bytes.HasPrefix(h, []byte{0x1f, 0x8b}):
		return "gz"
	case bytes.HasPrefix(h, []byte("ID3")), bytes.HasPrefix(h, []byte{0xff, 0xfb}), bytes.HasPrefix(h, []byte{0xff, 0xf3}):
		return "mp3"
	case bytes.HasPrefix(h, []byte("fLaC")):
		return "flac"
	case bytes.HasPrefix(h, []byte("OggS")):
		return "ogg"
	case len(h) > 12 && bytes.HasPrefix(h, []byte("RIFF")) && string(h[8:12]) == "WAVE":
		return "wav"
	case bytes.HasPrefix(h, []byte("wOFF")):
		return "woff"
	case bytes.HasPrefix(h, []byte("wOF2")):
		return "woff2"
	case bytes.HasPrefix(h, []byte("OTTO")):
		return "otf"
	case bytes.HasPrefix(h, []byte{0, 1, 0, 0, 0}):
		return "ttf"
	case bytes.HasPrefix(h, []byte("ttcf")):
		return "ttc"
	case bytes.HasPrefix(h, []byte("PK\x03\x04")):
		return sniffZip(b)
	case bytes.HasPrefix(h, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}):
		return sniffOLE(b)
	case len(h) > 12 && string(h[4:8]) == "ftyp":
		// ISO BMFF is also HEIC/AVIF stills; only the video brands go to ffmpeg.
		switch string(h[8:12]) {
		case "heic", "heix", "mif1", "msf1":
			return "heic"
		case "avif", "avis":
			return "avif"
		case "qt  ":
			return "mov"
		}
		return "mp4"
	case bytes.HasPrefix(h, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		return "webm" // EBML: WebM and Matroska
	case len(h) > 12 && bytes.HasPrefix(h, []byte("RIFF")) && string(h[8:12]) == "AVI ":
		return "avi"
	}
	if !utf8.Valid(h) {
		return ""
	}
	t := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(string(h), "\ufeff")))
	switch {
	case strings.HasPrefix(t, "<!doctype html"), strings.HasPrefix(t, "<html"):
		return "html"
	case strings.HasPrefix(t, "<svg"), strings.HasPrefix(t, "<?xml") && strings.Contains(t, "<svg"):
		return "svg"
	case looksLikeEmail(t):
		return "eml"
	case strings.HasPrefix(t, "begin:vcalendar"):
		return "ics"
	case strings.HasPrefix(t, "begin:vcard"):
		return "vcf"
	case strings.HasPrefix(t, "{") && strings.Contains(t, `"nbformat"`), strings.HasPrefix(t, "{") && strings.Contains(t, `"cells"`):
		return "ipynb"
	case strings.HasPrefix(t, "{") || strings.HasPrefix(t, "["):
		return "json"
	case isGraphviz(b):
		return "gv"
	case looksLikeCSV(t):
		return "csv"
	}
	return ""
}

// looksLikeCSV: at least three lines with the same non-zero count of one delimiter.
func looksLikeCSV(t string) bool {
	lines := strings.Split(strings.TrimSpace(t), "\n")
	if len(lines) > 1 && !strings.HasSuffix(t, "\n") {
		lines = lines[:len(lines)-1] // the 512-byte head usually cuts the last line
	}
	if len(lines) < 3 {
		return false
	}
	for _, d := range []string{",", ";", "\t"} {
		n := strings.Count(lines[0], d)
		if n == 0 {
			continue
		}
		ok := true
		for _, l := range lines[1:] {
			if strings.Count(l, d) != n {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func looksLikeEmail(t string) bool {
	hits := 0
	for _, hdr := range []string{"\nfrom:", "\nto:", "\nsubject:", "\ndate:", "\nmime-version:", "\nreceived:", "\nmessage-id:"} {
		if strings.Contains("\n"+t, hdr) {
			hits++
		}
	}
	return hits >= 3
}

// sniffOLE tells legacy Office files and Outlook messages apart by their
// top-level Compound File streams.
func sniffOLE(b []byte) (ext string) {
	// mscfb panics on some malformed headers (found by FuzzDetect): treat them as not OLE.
	defer func() {
		if recover() != nil {
			ext = ""
		}
	}()
	r, err := mscfb.New(bytes.NewReader(b))
	if err != nil {
		return ""
	}
	for f, err := r.Next(); err == nil; f, err = r.Next() {
		if len(f.Path) > 0 {
			continue
		}
		switch {
		case f.Name == "WordDocument":
			return "doc"
		case f.Name == "Workbook" || f.Name == "Book":
			return "xls"
		case f.Name == "PowerPoint Document":
			return "ppt"
		case f.Name == "VisioDocument":
			return "vsd"
		case f.Name == "Quill":
			return "pub"
		case f.Name == "__properties_version1.0" || strings.HasPrefix(f.Name, "__substg1.0_"):
			return "msg"
		}
	}
	return ""
}

// sniffZip tells OOXML, ODF and EPUB apart by member names.
func sniffZip(b []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return ""
	}
	for _, f := range zr.File {
		if f.Name == "mimetype" {
			rc, err := f.Open()
			if err != nil {
				break
			}
			mt, _ := io.ReadAll(io.LimitReader(rc, 128))
			rc.Close()
			switch strings.TrimSpace(string(mt)) {
			case "application/vnd.oasis.opendocument.text":
				return "odt"
			case "application/vnd.oasis.opendocument.spreadsheet":
				return "ods"
			case "application/vnd.oasis.opendocument.presentation":
				return "odp"
			case "application/vnd.oasis.opendocument.graphics":
				return "odg"
			case "application/epub+zip":
				return "epub"
			}
		}
	}
	for _, f := range zr.File {
		switch {
		case strings.HasPrefix(f.Name, "word/"):
			return "docx"
		case strings.HasPrefix(f.Name, "xl/"):
			return "xlsx"
		case strings.HasPrefix(f.Name, "ppt/"):
			return "pptx"
		case strings.HasPrefix(f.Name, "visio/"):
			return "vsdx"
		}
	}
	return "zip"
}

type formatGroup struct {
	Route      route    `json:"route"`
	Extensions []string `json:"extensions"`
	Note       string   `json:"note"`
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func Formats() []formatGroup {
	return []formatGroup{
		{PDF, []string{"pdf"}, "rasterised directly by PDFium"},
		{TIFF, []string{"tif", "tiff"}, "tiff2pdf keeps every page (scans, faxes); odd TIFFs fall back to one image"},
		{DjVu, []string{"djv", "djvu"}, "ddjvu → PDF"},
		{XPS, []string{"oxps", "xps"}, "xpstopdf → PDF (vector)"},
		{Image, keys(imageExts), "decoded (Go, or ffmpeg for heic/avif/jxl/psd/…) and re-encoded; animated GIF/WebP: page = frame"},
		{Raw, keys(rawExts), "camera RAW: the embedded JPEG preview (LibRaw), else a half-size develop"},
		{Office, keys(officeExts), "LibreOffice → PDF"},
		{Sheet, keys(sheetExts), "LibreOffice → PDF, one page per sheet"},
		{Table, []string{"csv", "tab", "tsv"}, "HTML table, header repeated on every page, A4 landscape"},
		{SVG, []string{"svg"}, "Chromium, one page of the SVG's own size → vector"},
		{Graphviz, []string{"dot", "gv"}, "Graphviz dot → vector PDF (.dot: Graphviz text vs Word template by content)"},
		{HTML, keys(htmlExts), "Chromium → A4 PDF; JavaScript and network are off"},
		{Markdown, keys(markdownExts), "Chromium → A4 PDF"},
		{Notebook, []string{"ipynb"}, "Jupyter notebook: markdown, code, outputs and plots; nothing is executed"},
		{Email, keys(emailExts), "headers + HTML body, inline images → Chromium; mht/mhtml without the mail header"},
		{Msg, []string{"msg"}, "Outlook: MAPI properties, HTML or RTF body (encapsulated HTML unwrapped), inline images"},
		{EPUB, []string{"epub"}, "chapters in spine order, images inlined → Chromium"},
		{Calendar, []string{"ics", "ifb"}, "agenda: one card per event"},
		{Contacts, []string{"vcard", "vcf"}, "one card per contact, with the embedded photo"},
		{Font, keys(fontExts), "specimen: name, alphabet, diacritics, sizes 12–72 px"},
		{JSON, []string{"geojson", "json"}, "pretty-printed listing"},
		{Text, keys(textExts), "monospaced listing → Chromium; UTF-8, else Windows-1250; first 256 KiB"},
		{Video, keys(VideoExts), "ffmpeg: one frame (time=, default 10 %); /render/grid spreads frames over the video"},
		{Audio, keys(audioExts), "card: cover art, title/artist/album, duration, waveform"},
		{Archive, append(keys(ArchiveExts), "tar.bz2", "tar.gz", "tar.xz", "tar.zst"), "the files inside, each through its own pipeline; gz/xz/… alone = one file"},
	}
}
