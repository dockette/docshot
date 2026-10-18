package htmlconv

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/richardlehane/mscfb"
	"golang.org/x/text/encoding/charmap"
)

// msgProps maps __substg1.0_<TAG><TYPE> streams ("0037001F") to raw bytes; type 001F is UTF-16LE, 001E 8-bit, 0102 binary.
// Fixed-size values (dates) live in __properties_version1.0; attachments are __attach_version1.0_#NNNNNNNN storages.
type msgProps map[string][]byte

func (p msgProps) str(tag string) string {
	if b, ok := p[tag+"001F"]; ok {
		return utf16le(b)
	}
	if b, ok := p[tag+"001E"]; ok {
		if s, err := charmap.Windows1252.NewDecoder().Bytes(bytes.TrimRight(b, "\x00")); err == nil {
			return string(s)
		}
		return string(bytes.TrimRight(b, "\x00"))
	}
	return ""
}

func utf16le(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}

type msgAttachment struct {
	name, mime, cid string
	data            []byte
}

func MsgToHTML(raw []byte) (string, error) {
	r, err := mscfb.New(bytes.NewReader(raw))
	if err != nil {
		return "", apierr.New(http.StatusUnprocessableEntity, "not an Outlook message: %v", err)
	}
	top := msgProps{}
	attach := map[string]msgProps{}
	var propsStream []byte
	for f, err := r.Next(); err == nil; f, err = r.Next() {
		if f.Size > 64<<20 {
			continue
		}
		data, _ := io.ReadAll(f)
		switch {
		case len(f.Path) == 0 && strings.HasPrefix(f.Name, "__substg1.0_"):
			top[strings.ToUpper(strings.TrimPrefix(f.Name, "__substg1.0_"))] = data
		case len(f.Path) == 0 && f.Name == "__properties_version1.0":
			propsStream = data
		case len(f.Path) == 1 && strings.HasPrefix(f.Path[0], "__attach_version1.0_") && strings.HasPrefix(f.Name, "__substg1.0_"):
			a := attach[f.Path[0]]
			if a == nil {
				a = msgProps{}
				attach[f.Path[0]] = a
			}
			a[strings.ToUpper(strings.TrimPrefix(f.Name, "__substg1.0_"))] = data
		}
	}
	if len(top) == 0 {
		return "", apierr.New(http.StatusUnprocessableEntity, "not an Outlook message: no MAPI properties")
	}

	var atts []msgAttachment
	for _, a := range attach {
		atts = append(atts, msgAttachment{
			name: cmp.Or(a.str("3707"), a.str("3704")),
			mime: a.str("370E"),
			cid:  strings.Trim(a.str("3712"), "<>"),
			data: a["37010102"],
		})
	}

	from := top.str("0C1A")
	if email := cmp.Or(top.str("5D01"), top.str("0C1F")); email != "" && !strings.Contains(from, "@") {
		from = strings.TrimSpace(from + " <" + email + ">")
	}
	hdr := map[string]string{
		"Subject": top.str("0037"),
		"From":    from,
		"To":      top.str("0E04"),
		"Cc":      top.str("0E03"),
		"Date":    msgDate(propsStream),
	}

	p := &mailParts{inline: map[string]string{}, located: map[string]string{}}
	for _, a := range atts {
		mt := cmp.Or(a.mime, mimeByName(a.name))
		if a.cid != "" && strings.HasPrefix(mt, "image/") && len(a.data) > 0 {
			p.inline[a.cid] = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(a.data)
			continue
		}
		if a.name != "" {
			p.attachments = append(p.attachments, a.name)
		}
	}

	switch {
	case len(top["10130102"]) > 0:
		p.html = DecodeText(top["10130102"])
	case top.str("1013") != "":
		p.html = top.str("1013")
	case len(top["10090102"]) > 0:
		if rtf, err := decompressRTF(top["10090102"]); err == nil {
			if h, isHTML := rtfToText(rtf); isHTML {
				p.html = h
			} else if top.str("1000") == "" {
				p.text = h
			}
		}
	}
	if p.html == "" && p.text == "" {
		p.text = top.str("1000")
	}
	return mailPage(func(k string) string { return hdr[k] }, p, false), nil
}

// msgDate: PR_CLIENT_SUBMIT_TIME (0x0039) or PR_MESSAGE_DELIVERY_TIME (0x0E06)
// from the top-level property stream (32-byte header, then 16-byte entries).
func msgDate(ps []byte) string {
	if len(ps) < 32 {
		return ""
	}
	var found time.Time
	for off := 32; off+16 <= len(ps); off += 16 {
		tag := binary.LittleEndian.Uint32(ps[off:])
		if tag&0xFFFF != 0x0040 {
			continue
		}
		id := tag >> 16
		if id != 0x0039 && id != 0x0E06 {
			continue
		}
		ft := binary.LittleEndian.Uint64(ps[off+8:])
		t := time.Unix(0, 0).Add(time.Duration(ft-116444736000000000) * 100)
		if id == 0x0039 || found.IsZero() {
			found = t
		}
	}
	if found.IsZero() {
		return ""
	}
	return found.UTC().Format("Mon, 2 Jan 2006 15:04:05 MST")
}

func mimeByName(n string) string {
	switch strings.ToLower(n[strings.LastIndex(n, ".")+1:]) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	}
	return ""
}

const rtfPrebuf = "{\\rtf1\\ansi\\mac\\deff0\\deftab720{\\fonttbl;}{\\f0\\fnil \\froman \\fswiss \\fmodern \\fscript \\fdecor MS Sans SerifSymbolArialTimes New RomanCourier{\\colortbl\\red0\\green0\\blue0\r\n\\par \\pard\\plain\\f0\\fs20\\b\\i\\u\\tab\\tx"

func decompressRTF(b []byte) ([]byte, error) {
	if len(b) < 16 {
		return nil, fmt.Errorf("short RTF header")
	}
	rawSize := binary.LittleEndian.Uint32(b[4:])
	switch string(b[8:12]) {
	case "MELA": // stored uncompressed
		return b[16:min(len(b), 16+int(rawSize))], nil
	case "LZFu":
	default:
		return nil, fmt.Errorf("unknown RTF compression")
	}
	var dict [4096]byte
	copy(dict[:], rtfPrebuf)
	wpos := len(rtfPrebuf)
	out := make([]byte, 0, rawSize)
	in := b[16:]
	for i := 0; i < len(in); {
		ctrl := in[i]
		i++
		for bit := 0; bit < 8 && i < len(in); bit++ {
			if ctrl&(1<<bit) == 0 {
				c := in[i]
				i++
				out = append(out, c)
				dict[wpos] = c
				wpos = (wpos + 1) % 4096
				continue
			}
			if i+1 >= len(in) {
				return out, nil
			}
			ref := int(in[i])<<8 | int(in[i+1])
			i += 2
			offset, length := ref>>4, ref&0xF+2
			if offset == wpos { // end marker
				return out, nil
			}
			for k := 0; k < length; k++ {
				c := dict[(offset+k)%4096]
				out = append(out, c)
				dict[wpos] = c
				wpos = (wpos + 1) % 4096
			}
		}
	}
	return out, nil
}

// rtfToText walks the RTF: when it carries \fromhtml (Outlook's encapsulated
// HTML, MS-OXRTFEX) it returns the original HTML; otherwise plain text.
func rtfToText(rtf []byte) (string, bool) {
	s := string(rtf)
	isHTML := strings.Contains(s[:min(len(s), 400)], `\fromhtml`)
	type frame struct{ skip, htmltag bool }
	stack := []frame{{}}
	var out bytes.Buffer
	htmlrtf := false
	ucSkip := 0
	cp := charmap.Windows1252 // \ansicpgNNNN switches it: Czech mail is 1250
	codepages := map[string]*charmap.Charmap{
		"874": charmap.Windows874, "1250": charmap.Windows1250, "1251": charmap.Windows1251, "1252": charmap.Windows1252,
		"1253": charmap.Windows1253, "1254": charmap.Windows1254, "1255": charmap.Windows1255, "1256": charmap.Windows1256,
		"1257": charmap.Windows1257, "1258": charmap.Windows1258,
	}
	emit := func(b byte) {
		f := stack[len(stack)-1]
		if f.htmltag || (!f.skip && !htmlrtf) {
			out.WriteByte(b)
		}
	}
	emitStr := func(t string) {
		for i := 0; i < len(t); i++ {
			emit(t[i])
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '{':
			stack = append(stack, stack[len(stack)-1])
			i++
		case '}':
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			i++
		case '\\':
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '\\', '{', '}':
				emit(s[i])
				i++
				continue
			case '\'':
				if i+2 < len(s) {
					if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
						if dec, err := cp.NewDecoder().Bytes([]byte{byte(v)}); err == nil {
							emitStr(string(dec))
						}
					}
				}
				i += 3
				continue
			case '*':
				// \*\htmltag carries HTML; any other \* destination is ignorable.
				if strings.HasPrefix(s[i:], `*\htmltag`) {
					stack[len(stack)-1].htmltag = true
				} else {
					stack[len(stack)-1].skip = true
				}
				i++
				continue
			}
			j := i
			for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
				j++
			}
			word := s[i:j]
			k := j
			if k < len(s) && (s[k] == '-' || s[k] >= '0' && s[k] <= '9') {
				k++
				for k < len(s) && s[k] >= '0' && s[k] <= '9' {
					k++
				}
			}
			param := s[j:k]
			if k < len(s) && s[k] == ' ' {
				k++
			}
			i = k
			if word == "" { // control symbol like \~ or \-
				if i < len(s) {
					i++
				}
				continue
			}
			switch word {
			case "htmlrtf":
				htmlrtf = param != "0"
			case "ansicpg":
				if c, ok := codepages[param]; ok {
					cp = c
				}
			case "par", "line":
				if !isHTML {
					emit('\n')
				} else if !htmlrtf && !stack[len(stack)-1].htmltag {
					emitStr("\r\n")
				}
			case "tab":
				emit('\t')
			case "u":
				n, _ := strconv.Atoi(param)
				if n < 0 {
					n += 65536
				}
				emitStr(string(rune(n)))
				ucSkip = 1
			case "fonttbl", "colortbl", "stylesheet", "info", "pict", "header", "footer", "themedata", "datastore", "latentstyles":
				stack[len(stack)-1].skip = true
			}
		case '\r', '\n':
			i++
		default:
			if ucSkip > 0 {
				ucSkip--
				i++
				continue
			}
			emit(c)
			i++
		}
	}
	return out.String(), isHTML
}
