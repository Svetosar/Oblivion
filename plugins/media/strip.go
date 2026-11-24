// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// Metadata stripping. Uploaded photos carry EXIF (GPS position, camera serial,
// timestamps), XMP, comments and thumbnails. We rewrite the container and drop
// all of it; image data is copied byte for byte, so there is no re-encoding
// loss. Anything malformed is rejected rather than passed through.

var errBadImage = errors.New("malformed image")

type imgKind struct {
	mime, ext string
}

var (
	kJPEG = imgKind{"image/jpeg", ".jpg"}
	kPNG  = imgKind{"image/png", ".png"}
	kGIF  = imgKind{"image/gif", ".gif"}
	kWEBP = imgKind{"image/webp", ".webp"}
)

// sniff identifies the type by magic bytes only (never by name or client header).
func sniff(b []byte) (imgKind, bool) {
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return kJPEG, true
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return kPNG, true
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return kGIF, true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return kWEBP, true
	}
	return imgKind{}, false
}

func strip(k imgKind, b []byte) ([]byte, error) {
	switch k {
	case kJPEG:
		return stripJPEG(b)
	case kPNG:
		return stripPNG(b)
	case kGIF:
		return stripGIF(b)
	case kWEBP:
		return stripWEBP(b)
	}
	return nil, errBadImage
}

// ---- JPEG ------------------------------------------------------------------

func stripJPEG(b []byte) ([]byte, error) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, errBadImage
	}
	out := make([]byte, 0, len(b))
	out = append(out, 0xFF, 0xD8)
	orientation := 0
	var body []byte // segments to keep, in order
	i := 2
	for {
		if i+2 > len(b) || b[i] != 0xFF {
			return nil, errBadImage
		}
		m := b[i+1]
		if m == 0xFF { // fill byte
			i++
			continue
		}
		if m == 0xD9 { // EOI before SOS: no image data
			return nil, errBadImage
		}
		if m == 0x01 || (m >= 0xD0 && m <= 0xD7) || m == 0xD8 {
			return nil, errBadImage // standalone markers are invalid in the header part
		}
		if i+4 > len(b) {
			return nil, errBadImage
		}
		l := int(binary.BigEndian.Uint16(b[i+2:]))
		if l < 2 || i+2+l > len(b) {
			return nil, errBadImage
		}
		seg := b[i : i+2+l]
		payload := seg[4:]
		keep := true
		switch {
		case m == 0xE1: // EXIF / XMP
			if o := exifOrientation(payload); o > orientation {
				orientation = o
			}
			keep = false
		case m == 0xE2: // keep ICC colour profiles only
			keep = bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00"))
		case m == 0xE0 || m == 0xEE: // JFIF, Adobe (colour transform): needed to decode
		case m >= 0xE3 && m <= 0xEF, m == 0xFE: // other APPn, comments
			keep = false
		}
		if keep {
			body = append(body, seg...)
		}
		i += 2 + l
		if m == 0xDA { // SOS: entropy-coded data follows; copy the rest verbatim
			rest := b[i:]
			if !bytes.Contains(rest, []byte{0xFF, 0xD9}) {
				return nil, errBadImage
			}
			out = append(out, orientationSegment(orientation)...)
			out = append(out, body...)
			return append(out, rest...), nil
		}
	}
}

// exifOrientation reads tag 0x0112 from an APP1 EXIF payload; 0 if absent/invalid.
func exifOrientation(p []byte) int {
	if !bytes.HasPrefix(p, []byte("Exif\x00\x00")) {
		return 0
	}
	t := p[6:]
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[off:]))
	for e := 0; e < n; e++ {
		at := off + 2 + e*12
		if at+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[at:]) == 0x0112 && bo.Uint16(t[at+2:]) == 3 && bo.Uint32(t[at+4:]) == 1 {
			if v := int(bo.Uint16(t[at+8:])); v >= 2 && v <= 8 {
				return v
			}
			return 0
		}
	}
	return 0
}

// orientationSegment builds an APP1 holding only the orientation, so rotated
// photos still display upright after the rest of the EXIF is gone.
func orientationSegment(o int) []byte {
	if o < 2 || o > 8 {
		return nil
	}
	seg := []byte{0xFF, 0xE1, 0, 34}
	seg = append(seg, "Exif\x00\x00MM\x00\x2A\x00\x00\x00\x08"...)
	seg = append(seg, 0, 1)                         // one IFD entry
	seg = append(seg, 0x01, 0x12, 0, 3, 0, 0, 0, 1) // tag, SHORT, count 1
	seg = append(seg, 0, byte(o), 0, 0)             // value
	seg = append(seg, 0, 0, 0, 0)                   // no next IFD
	return seg
}

// ---- PNG -------------------------------------------------------------------

var pngKeep = map[string]bool{
	"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true, "tRNS": true, "gAMA": true,
	"cHRM": true, "sRGB": true, "iCCP": true, "sBIT": true, "bKGD": true, "pHYs": true,
	"acTL": true, "fcTL": true, "fdAT": true,
}

func stripPNG(b []byte) ([]byte, error) {
	const sig = "\x89PNG\r\n\x1a\n"
	if !bytes.HasPrefix(b, []byte(sig)) {
		return nil, errBadImage
	}
	out := append(make([]byte, 0, len(b)), sig...)
	i, first, sawIDAT := len(sig), true, false
	for {
		if i+12 > len(b) {
			return nil, errBadImage
		}
		n := int(binary.BigEndian.Uint32(b[i:]))
		if n < 0 || n > len(b) || i+12+n > len(b) {
			return nil, errBadImage
		}
		typ := string(b[i+4 : i+8])
		if first != (typ == "IHDR") {
			return nil, errBadImage
		}
		first = false
		chunk := b[i : i+12+n]
		i += 12 + n
		if typ == "IDAT" {
			sawIDAT = true
		}
		if pngKeep[typ] {
			out = append(out, chunk...)
		} else if typ[0] >= 'A' && typ[0] <= 'Z' {
			return nil, errBadImage // unknown critical chunk
		}
		if typ == "IEND" {
			if !sawIDAT {
				return nil, errBadImage
			}
			return out, nil // anything after IEND is dropped
		}
	}
}

// ---- GIF -------------------------------------------------------------------

func stripGIF(b []byte) ([]byte, error) {
	if len(b) < 13 {
		return nil, errBadImage
	}
	out := append(make([]byte, 0, len(b)), b[:13]...)
	i := 13
	if b[10]&0x80 != 0 {
		n := 3 * (1 << (int(b[10]&7) + 1))
		if i+n > len(b) {
			return nil, errBadImage
		}
		out, i = append(out, b[i:i+n]...), i+n
	}
	// subBlocks returns the end of a sub-block chain starting at i.
	subBlocks := func(i int) (int, error) {
		for {
			if i >= len(b) {
				return 0, errBadImage
			}
			n := int(b[i])
			i += 1 + n
			if n == 0 {
				return i, nil
			}
		}
	}
	frames := 0
	for {
		if i >= len(b) {
			return nil, errBadImage
		}
		switch b[i] {
		case 0x3B: // trailer
			if frames == 0 {
				return nil, errBadImage
			}
			return append(out, 0x3B), nil
		case 0x21: // extension
			if i+2 > len(b) {
				return nil, errBadImage
			}
			label := b[i+1]
			end, err := subBlocks(i + 2)
			if err != nil {
				return nil, err
			}
			keep := label == 0xF9 // graphic control (timing, transparency)
			if label == 0xFF && i+14 <= len(b) && b[i+2] == 11 {
				id := string(b[i+3 : i+14])
				keep = id == "NETSCAPE2.0" || id == "ANIMEXTS1.0" // loop count only
			}
			if keep {
				out = append(out, b[i:end]...)
			}
			i = end
		case 0x2C: // image
			if i+10 > len(b) {
				return nil, errBadImage
			}
			start, j := i, i+10
			if b[i+9]&0x80 != 0 {
				j += 3 * (1 << (int(b[i+9]&7) + 1))
			}
			if j+1 > len(b) {
				return nil, errBadImage
			}
			end, err := subBlocks(j + 1) // skip LZW min code size
			if err != nil {
				return nil, err
			}
			out = append(out, b[start:end]...)
			i = end
			frames++
		default:
			return nil, errBadImage
		}
	}
}

// ---- WebP ------------------------------------------------------------------

func stripWEBP(b []byte) ([]byte, error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return nil, errBadImage
	}
	riffEnd := 8 + int(binary.LittleEndian.Uint32(b[4:]))
	if riffEnd > len(b) || riffEnd < 12 {
		return nil, errBadImage
	}
	out := append(make([]byte, 0, riffEnd), b[:12]...)
	i, image := 12, false
	for i < riffEnd {
		if i+8 > riffEnd {
			return nil, errBadImage
		}
		n := int(binary.LittleEndian.Uint32(b[i+4:]))
		end := i + 8 + n + n&1 // payloads are padded to even length
		if n < 0 || end > riffEnd || n > len(b) {
			return nil, errBadImage
		}
		tag := string(b[i : i+4])
		switch tag {
		case "EXIF", "XMP ":
			// dropped
		case "VP8X":
			if n < 10 {
				return nil, errBadImage
			}
			chunk := append([]byte(nil), b[i:end]...)
			chunk[8] &^= 0x08 | 0x04 // clear the EXIF and XMP flags
			out = append(out, chunk...)
		default:
			if tag == "VP8 " || tag == "VP8L" || tag == "ANMF" {
				image = true
			}
			out = append(out, b[i:end]...)
		}
		i = end
	}
	if !image {
		return nil, errBadImage
	}
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	return out, nil
}
