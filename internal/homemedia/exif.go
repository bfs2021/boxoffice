package homemedia

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"time"
)

// Meta is what a photo says about itself in its EXIF: when and where it
// was taken, on what, and which way up it is.
type Meta struct {
	Taken       time.Time // the camera's clock, read as UTC; zero if it didn't say
	Make, Model string
	Lat, Lon    float64
	HasGPS      bool
	Orientation int // 1-8, 0 when it didn't say
	Width       int
	Height      int
	// Previews are JPEGs kept inside the file, such as a raw photo's:
	// where each starts and how long it is.
	Previews [][2]int64
}

// EXIF tags read.
const (
	tagMake          = 0x010F
	tagModel         = 0x0110
	tagOrientation   = 0x0112
	tagDateTime      = 0x0132
	tagSubIFDs       = 0x014A
	tagJPEGOffset    = 0x0201
	tagJPEGLength    = 0x0202
	tagExifIFD       = 0x8769
	tagGPSIFD        = 0x8825
	tagDateOriginal  = 0x9003
	tagDateDigitized = 0x9004
	tagPixelX        = 0xA002
	tagPixelY        = 0xA003
	tagImageWidth    = 0x0100
	tagImageHeight   = 0x0101
	tagGPSLatRef     = 1
	tagGPSLat        = 2
	tagGPSLonRef     = 3
	tagGPSLon        = 4
)

// typeSize is how many bytes each TIFF value type takes.
var typeSize = map[uint16]int64{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8, 13: 4}

type tiff struct {
	r     io.ReaderAt
	base  int64 // where the TIFF header is
	order binary.ByteOrder
}

type ifdEntry struct {
	tag, typ uint16
	count    uint32
	raw      [4]byte
}

// newTIFF reads a TIFF header at base.
func newTIFF(r io.ReaderAt, base int64) (*tiff, uint32, error) {
	var h [8]byte
	if _, err := r.ReadAt(h[:], base); err != nil {
		return nil, 0, err
	}
	var order binary.ByteOrder
	switch {
	case h[0] == 'I' && h[1] == 'I':
		order = binary.LittleEndian
	case h[0] == 'M' && h[1] == 'M':
		order = binary.BigEndian
	default:
		return nil, 0, errors.New("not a TIFF header")
	}
	// 42 for TIFF; Olympus (0x4F52, 0x5352) and Panasonic (0x55) raws
	// keep TIFF's layout under another number.
	switch order.Uint16(h[2:]) {
	case 42, 0x4F52, 0x5352, 0x55:
	default:
		return nil, 0, errors.New("not a TIFF header")
	}
	return &tiff{r: r, base: base, order: order}, order.Uint32(h[4:]), nil
}

// ifd reads the directory at off and says where the next one is.
func (t *tiff) ifd(off uint32) ([]ifdEntry, uint32, error) {
	var n [2]byte
	if _, err := t.r.ReadAt(n[:], t.base+int64(off)); err != nil {
		return nil, 0, err
	}
	count := int(t.order.Uint16(n[:]))
	if count == 0 || count > 1000 {
		return nil, 0, errors.New("bad TIFF directory")
	}
	buf := make([]byte, count*12+4)
	if _, err := t.r.ReadAt(buf, t.base+int64(off)+2); err != nil && !(errors.Is(err, io.EOF) && len(buf) >= count*12) {
		return nil, 0, err
	}
	out := make([]ifdEntry, count)
	for i := range out {
		e := buf[i*12:]
		out[i] = ifdEntry{tag: t.order.Uint16(e), typ: t.order.Uint16(e[2:]), count: t.order.Uint32(e[4:])}
		copy(out[i].raw[:], e[8:12])
	}
	next := t.order.Uint32(buf[count*12:])
	return out, next, nil
}

// data is an entry's value bytes.
func (t *tiff) data(e ifdEntry) ([]byte, error) {
	size := typeSize[e.typ] * int64(e.count)
	if size == 0 || size > 1<<16 {
		return nil, errors.New("bad TIFF value")
	}
	if size <= 4 {
		return e.raw[:size], nil
	}
	b := make([]byte, size)
	_, err := t.r.ReadAt(b, t.base+int64(t.order.Uint32(e.raw[:])))
	return b, err
}

// uint reads an entry's first whole number.
func (t *tiff) uint(e ifdEntry) (uint32, bool) {
	switch e.typ {
	case 3, 8:
		return uint32(t.order.Uint16(e.raw[:])), true
	case 4, 9, 13:
		return t.order.Uint32(e.raw[:]), true
	case 1, 6, 7:
		return uint32(e.raw[0]), true
	}
	return 0, false
}

// uints reads every whole number in an entry, such as SubIFDs' offsets.
func (t *tiff) uints(e ifdEntry) []uint32 {
	b, err := t.data(e)
	if err != nil {
		return nil
	}
	var out []uint32
	switch e.typ {
	case 4, 9, 13:
		for i := 0; i+4 <= len(b); i += 4 {
			out = append(out, t.order.Uint32(b[i:]))
		}
	case 3, 8:
		for i := 0; i+2 <= len(b); i += 2 {
			out = append(out, uint32(t.order.Uint16(b[i:])))
		}
	}
	return out
}

func (t *tiff) str(e ifdEntry) string {
	if e.typ != 2 {
		return ""
	}
	b, err := t.data(e)
	if err != nil {
		return ""
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// rationals reads an entry's fractions, such as GPS degrees.
func (t *tiff) rationals(e ifdEntry) []float64 {
	if e.typ != 5 && e.typ != 10 {
		return nil
	}
	b, err := t.data(e)
	if err != nil {
		return nil
	}
	var out []float64
	for i := 0; i+8 <= len(b); i += 8 {
		n, d := float64(t.order.Uint32(b[i:])), float64(t.order.Uint32(b[i+4:]))
		if e.typ == 10 {
			n, d = float64(int32(t.order.Uint32(b[i:]))), float64(int32(t.order.Uint32(b[i+4:])))
		}
		if d == 0 {
			out = append(out, 0)
			continue
		}
		out = append(out, n/d)
	}
	return out
}

// readTIFF reads what Meta holds from a TIFF structure: a JPEG's EXIF, or
// a raw photo, which is a TIFF itself.
func readTIFF(r io.ReaderAt, base int64) (Meta, error) {
	t, first, err := newTIFF(r, base)
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	var dateTime, original, digitized string
	var exifOff, gpsOff uint32
	seen := map[uint32]bool{}
	// IFD0 and those after it (IFD1 holds the thumbnail), and raw
	// photos' sub-IFDs, which hold the full picture and previews.
	queue := []uint32{first}
	for len(queue) > 0 && len(seen) < 16 {
		off := queue[0]
		queue = queue[1:]
		if off == 0 || seen[off] {
			continue
		}
		seen[off] = true
		entries, next, err := t.ifd(off)
		if err != nil {
			if off == first {
				return Meta{}, err
			}
			continue
		}
		var jpegOff, jpegLen uint32
		for _, e := range entries {
			switch e.tag {
			case tagMake:
				if off == first {
					m.Make = t.str(e)
				}
			case tagModel:
				if off == first {
					m.Model = t.str(e)
				}
			case tagOrientation:
				if v, ok := t.uint(e); ok && off == first && v >= 1 && v <= 8 {
					m.Orientation = int(v)
				}
			case tagDateTime:
				if off == first {
					dateTime = t.str(e)
				}
			case tagExifIFD:
				exifOff, _ = t.uint(e)
			case tagGPSIFD:
				gpsOff, _ = t.uint(e)
			case tagSubIFDs:
				queue = append(queue, t.uints(e)...)
			case tagJPEGOffset:
				jpegOff, _ = t.uint(e)
			case tagJPEGLength:
				jpegLen, _ = t.uint(e)
			case tagImageWidth:
				if v, ok := t.uint(e); ok && int(v) > m.Width && off == first {
					m.Width = int(v)
				}
			case tagImageHeight:
				if v, ok := t.uint(e); ok && int(v) > m.Height && off == first {
					m.Height = int(v)
				}
			}
		}
		if jpegOff > 0 && jpegLen > 0 {
			m.Previews = append(m.Previews, [2]int64{base + int64(jpegOff), int64(jpegLen)})
		}
		queue = append(queue, next)
	}
	if exifOff > 0 {
		if entries, _, err := t.ifd(exifOff); err == nil {
			for _, e := range entries {
				switch e.tag {
				case tagDateOriginal:
					original = t.str(e)
				case tagDateDigitized:
					digitized = t.str(e)
				case tagPixelX:
					if v, ok := t.uint(e); ok && v > 0 {
						m.Width = int(v)
					}
				case tagPixelY:
					if v, ok := t.uint(e); ok && v > 0 {
						m.Height = int(v)
					}
				}
			}
		}
	}
	for _, s := range []string{original, digitized, dateTime} {
		if tm, ok := exifTime(s); ok {
			m.Taken = tm
			break
		}
	}
	if gpsOff > 0 {
		if entries, _, err := t.ifd(gpsOff); err == nil {
			var latRef, lonRef string
			var lat, lon []float64
			for _, e := range entries {
				switch e.tag {
				case tagGPSLatRef:
					latRef = strings.ToUpper(string(bytes.Trim(e.raw[:], "\x00 ")))
				case tagGPSLonRef:
					lonRef = strings.ToUpper(string(bytes.Trim(e.raw[:], "\x00 ")))
				case tagGPSLat:
					lat = t.rationals(e)
				case tagGPSLon:
					lon = t.rationals(e)
				}
			}
			if la, ok := degrees(lat, latRef == "S"); ok {
				if lo, ok := degrees(lon, lonRef == "W"); ok && !(la == 0 && lo == 0) && math.Abs(la) <= 90 && math.Abs(lo) <= 180 {
					m.Lat, m.Lon, m.HasGPS = la, lo, true
				}
			}
		}
	}
	return m, nil
}

func degrees(v []float64, negative bool) (float64, bool) {
	if len(v) == 0 {
		return 0, false
	}
	d := v[0]
	if len(v) > 1 {
		d += v[1] / 60
	}
	if len(v) > 2 {
		d += v[2] / 3600
	}
	if negative {
		d = -d
	}
	return d, true
}

// exifTime reads EXIF's 2006:01:02 15:04:05, which has no time zone: the
// camera's clock is kept as it was, read as UTC.
func exifTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 19 || strings.HasPrefix(s, "0000") {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006:01:02 15:04:05", "2006-01-02 15:04:05", "2006:01:02T15:04:05"} {
		if t, err := time.Parse(layout, s[:19]); err == nil {
			if t.Year() < 1900 || t.Year() > 2200 {
				return time.Time{}, false
			}
			return t, true
		}
	}
	return time.Time{}, false
}

// readJPEG reads a JPEG's EXIF and its size in pixels.
func readJPEG(b []byte) (Meta, error) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return Meta{}, errors.New("not a JPEG")
	}
	var m Meta
	var w, h int
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			i++
			continue
		}
		marker := b[i+1]
		if marker == 0xFF {
			i++
			continue
		}
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			i += 2
			continue
		}
		if marker == 0xD9 || marker == 0xDA {
			break
		}
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if size < 2 || i+2+size > len(b) {
			break
		}
		seg := b[i+4 : i+2+size]
		switch {
		case marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) && m.Taken.IsZero() && m.Make == "":
			if em, err := readTIFF(bytes.NewReader(seg[6:]), 0); err == nil {
				em.Previews = nil // offsets inside the segment, not the file
				m = em
			}
		case marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC && len(seg) >= 5 && w == 0:
			h = int(binary.BigEndian.Uint16(seg[1:]))
			w = int(binary.BigEndian.Uint16(seg[3:]))
		}
		i += 2 + size
	}
	if w > 0 && h > 0 {
		m.Width, m.Height = w, h
	}
	return m, nil
}

var exifMarker = []byte("Exif\x00\x00")

// findEXIF looks for an EXIF block anywhere in the start of a file, as
// HEIC, WebP and PNG files keep one in a box or chunk of their own.
func findEXIF(b []byte) (Meta, bool) {
	for off := 0; off < len(b); {
		i := bytes.Index(b[off:], exifMarker)
		if i < 0 {
			break
		}
		at := off + i + len(exifMarker)
		if m, err := readTIFF(bytes.NewReader(b), int64(at)); err == nil {
			m.Previews = nil
			return m, true
		}
		off = at
	}
	// A WebP or PNG EXIF chunk can start with the TIFF header itself.
	for _, h := range [][]byte{[]byte("II*\x00"), []byte("MM\x00*")} {
		if i := bytes.Index(b, h); i >= 0 && i < 1<<16 {
			if m, err := readTIFF(bytes.NewReader(b), int64(i)); err == nil && (!m.Taken.IsZero() || m.Make != "") {
				m.Previews = nil
				return m, true
			}
		}
	}
	return Meta{}, false
}
