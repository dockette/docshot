package media

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/jpeg"
	"io"
	"net/http"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/imaging"
)

// decodeBroken shows what survived of a truncated or corrupted image (cut-off downloads, payloads carved
// from a capture), like browsers do: Go decodes a repaired copy, else ffmpeg tries; cause is Go's error.
// The caller has already checked the header's size against the pixel limit.
func decodeBroken(ctx context.Context, body []byte, cfg image.Config, format, ext string, cause error) (image.Image, error) {
	if fixed := salvage(body, cfg, format); fixed != nil {
		if img, err := decodeImage(fixed); err == nil {
			return img, nil
		}
	}
	img, err := ffmpegImage(ctx, body, ext) // JPEG: decodes the partial scan, rest grey
	if err == nil || ctx.Err() != nil {
		return img, err
	}
	if format == "jpeg" {
		if img := salvageJPEG(body); img != nil {
			return img, nil
		}
	}
	return nil, apierr.New(http.StatusUnprocessableEntity, "decoding %s: %v", format, cause)
}

// salvage repairs a broken PNG, BMP, WebP or GIF for the Go decoders; nil when there is nothing to repair.
func salvage(body []byte, cfg image.Config, format string) []byte {
	switch format {
	case "png":
		return salvagePNG(body)
	case "bmp":
		return salvageBMP(body)
	case "webp":
		return salvageWebP(body, cfg)
	case "gif":
		return salvageGIF(body)
	}
	return nil
}

// salvagePNG ignores CRCs (as Chromium does) and a missing IEND, inflates IDAT up to the first error,
// pads the missing rows (transparent/black) and re-encodes the result.
func salvagePNG(b []byte) []byte {
	const sig = "\x89PNG\r\n\x1a\n"
	if len(b) < len(sig) || string(b[:len(sig)]) != sig {
		return nil
	}
	var ihdr, plte, trns, idat []byte
	for p := len(sig); p+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[p:]))
		typ := b[p+4 : p+8]
		if !isChunkType(typ) {
			break // garbage instead of a chunk header: the rest is lost
		}
		data := b[p+8 : p+8+min(n, len(b)-p-8)]
		switch string(typ) {
		case "IHDR":
			ihdr = data
		case "PLTE":
			plte = data
		case "tRNS":
			trns = data
		case "IDAT":
			idat = append(idat, data...)
		}
		if string(typ) == "IEND" || n > len(b)-p-12 {
			break
		}
		p += 12 + n
	}
	if len(ihdr) != 13 || len(idat) < 2 {
		return nil
	}

	w, h := int(binary.BigEndian.Uint32(ihdr[0:])), int(binary.BigEndian.Uint32(ihdr[4:]))
	channels := map[byte]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[ihdr[9]]
	bits := channels * int(ihdr[8])
	if w <= 0 || h <= 0 || bits == 0 || int64(w)*int64(h) > 3*imaging.MaxPixels {
		return nil
	}
	// rows of the image, or of each Adam7 pass; every row starts with its filter byte
	var rows []int
	addPass := func(pw, ph int) {
		if pw > 0 && ph > 0 {
			for range ph {
				rows = append(rows, 1+(pw*bits+7)/8)
			}
		}
	}
	if ihdr[12] == 1 {
		for _, a := range [][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}} {
			addPass((w-a[0]+a[2]-1)/a[2], (h-a[1]+a[3]-1)/a[3])
		}
	} else {
		addPass(w, h)
	}
	size := 0
	for _, r := range rows {
		size += r
	}

	// raw deflate after the zlib header: no Adler-32 check, and data up to a corrupted block survives
	raw := make([]byte, size)
	got, _ := io.ReadFull(flate.NewReader(bytes.NewReader(idat[2:])), raw)
	if got == 0 {
		return nil
	}
	for o, i := 0, 0; i < len(rows) && o < got; o, i = o+rows[i], i+1 {
		if raw[o] > 4 {
			raw[o] = 0 // corrupted filter type
		}
	}

	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestSpeed)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	out := bytes.NewBufferString(sig)
	writeChunk(out, "IHDR", ihdr)
	if plte != nil {
		writeChunk(out, "PLTE", plte)
	}
	if trns != nil {
		writeChunk(out, "tRNS", trns)
	}
	writeChunk(out, "IDAT", z.Bytes())
	writeChunk(out, "IEND", nil)
	return out.Bytes()
}

func isChunkType(t []byte) bool {
	for _, c := range t {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

func writeChunk(w *bytes.Buffer, typ string, data []byte) {
	_ = binary.Write(w, binary.BigEndian, uint32(len(data)))
	w.WriteString(typ)
	w.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	_ = binary.Write(w, binary.BigEndian, crc.Sum32())
}

// salvageBMP pads missing pixel rows of an uncompressed BMP with zeros.
func salvageBMP(b []byte) []byte {
	if len(b) < 34 || string(b[:2]) != "BM" || binary.LittleEndian.Uint32(b[30:]) != 0 {
		return nil
	}
	off := int64(binary.LittleEndian.Uint32(b[10:]))
	w := int64(int32(binary.LittleEndian.Uint32(b[18:])))
	h := int64(int32(binary.LittleEndian.Uint32(b[22:])))
	bpp := int64(binary.LittleEndian.Uint16(b[28:]))
	if h < 0 {
		h = -h // top-down
	}
	if w <= 0 || h == 0 || w*h > 3*imaging.MaxPixels || bpp == 0 || bpp > 32 || off > 1<<20 {
		return nil
	}
	return padTo(b, off+(w*bpp+31)/32*4*h)
}

// salvageWebP cuts an animation after its last complete frame, or pads a still (or the first,
// incomplete frame) to its declared size: lossy (VP8) data decodes as a blur, lossless does not.
func salvageWebP(b []byte, cfg image.Config) []byte {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return nil
	}
	end, first, firstN := 0, -1, 0
	for p := 12; p+8 <= len(b); {
		chunk, n := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:]))
		if chunk == "ANMF" && first < 0 {
			first, firstN = p, n
		}
		if n > len(b)-p-8 {
			break
		}
		p += 8 + n + n&1 // chunks are padded to an even size
		if chunk == "ANMF" {
			end = min(p, len(b))
		}
	}
	if end > 0 && end < len(b) {
		out := bytes.Clone(b[:end])
		binary.LittleEndian.PutUint32(out[4:], uint32(end-8))
		return out
	}
	if end == 0 && first >= 0 && firstN > 16 && first+24 < len(b) {
		// the frame's chunks (ALPH, VP8, VP8L) after its 16-byte header, as a still
		frame := b[first+24 : min(first+8+firstN, len(b))]
		b = binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(4+firstN-16))
		b = append(append(b, "WEBP"...), frame...)
	}
	// compressed data is never larger than the raw pixels: a lying RIFF size cannot force a huge pad
	n := int64(binary.LittleEndian.Uint32(b[4:])) + 8
	if n > 4*int64(cfg.Width)*int64(cfg.Height)+1<<16 {
		return nil
	}
	return padTo(b, n)
}

// salvageGIF cuts after the last complete frame; nil when even the first one is incomplete.
func salvageGIF(b []byte) []byte {
	if len(b) < 13 || string(b[:3]) != "GIF" {
		return nil
	}
	// skipBlocks returns the offset after a data sub-block chain, or -1 if it is cut off
	skipBlocks := func(p int) int {
		for p < len(b) {
			if b[p] == 0 {
				return p + 1
			}
			p += 1 + int(b[p])
		}
		return -1
	}
	p, end := 13, 0
	if b[10]&0x80 != 0 {
		p += 3 << (b[10]&7 + 1) // global colour table
	}
	for p >= 0 && p < len(b) {
		switch b[p] {
		case 0x21: // extension: introducer, label, sub-blocks
			p = skipBlocks(p + 2)
		case 0x2c: // image descriptor, local colour table, LZW code size, sub-blocks
			if p+10 > len(b) {
				p = -1
				break
			}
			flags := b[p+9]
			p += 10
			if flags&0x80 != 0 {
				p += 3 << (flags&7 + 1)
			}
			if p = skipBlocks(p + 1); p > 0 {
				end = p
			}
		default: // trailer or garbage
			p = -1
		}
	}
	if end == 0 || end >= len(b) {
		return nil
	}
	return append(bytes.Clone(b[:end]), 0x3b)
}

// salvageJPEG keeps the complete scans of a progressive JPEG (a coarser full image, as browsers
// show while loading): it ends the file before the last scans until Go decodes it.
func salvageJPEG(b []byte) image.Image {
	var scans []int
	for i := 2; i+1 < len(b); i++ {
		if b[i] == 0xff && b[i+1] == 0xda { // SOS cannot occur inside entropy-coded data (0xff is stuffed)
			scans = append(scans, i)
		}
	}
	for i, tries := len(scans)-1, 0; i > 0 && tries < 16; i, tries = i-1, tries+1 {
		if img, err := jpeg.Decode(bytes.NewReader(append(bytes.Clone(b[:scans[i]]), 0xff, 0xd9))); err == nil {
			return img
		}
	}
	return nil
}

// salvageICO keeps the largest icon of an ICO/CUR, its PNG repaired or its bitmap padded, for ffmpeg.
func salvageICO(b []byte) []byte {
	if len(b) < 22 || b[0] != 0 || b[1] != 0 || (b[2] != 1 && b[2] != 2) || b[3] != 0 {
		return nil
	}
	var best []byte
	var entry []byte
	area := -1
	for i, n := 0, int(binary.LittleEndian.Uint16(b[4:])); i < n && 6+16*i+16 <= len(b); i++ {
		e := b[6+16*i:][:16]
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size, off := int64(binary.LittleEndian.Uint32(e[8:])), int64(binary.LittleEndian.Uint32(e[12:]))
		if off >= int64(len(b)) || size > 1<<20 || w*h <= area {
			continue
		}
		data := b[off:min(off+size, int64(len(b)))]
		if bytes.HasPrefix(data, []byte("\x89PNG")) {
			data = salvagePNG(data)
		} else if int64(len(data)) < size {
			data = padTo(data, size)
		}
		if data != nil {
			best, entry, area = data, e, w*h
		}
	}
	if best == nil {
		return nil
	}
	out := append([]byte{0, 0, b[2], 0, 1, 0}, entry[:8]...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(best)))
	out = binary.LittleEndian.AppendUint32(out, 22)
	return append(out, best...)
}

// padTo appends zeros up to n bytes; nil when b is not shorter or n is beyond any sane image.
func padTo(b []byte, n int64) []byte {
	if n <= int64(len(b)) || n > 4*imaging.MaxPixels+1<<20 {
		return nil
	}
	return append(append(make([]byte, 0, n), b...), make([]byte, n-int64(len(b)))...)
}
