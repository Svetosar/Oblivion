// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/webp"

	"oblivion/internal/db"
	"oblivion/internal/plugins"
	"oblivion/sdk"
)

func testImage() *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for x := 0; x < 8; x++ {
		for y := 0; y < 6; y++ {
			im.Set(x, y, color.RGBA{uint8(x * 30), uint8(y * 40), 99, 255})
		}
	}
	return im
}

const gpsMarker = "SECRET-GPS-LOCATION"

// exifSegment builds an APP1 EXIF segment (big endian) with orientation o and a secret string.
func exifSegment(o int) []byte {
	tiff := []byte("MM\x00\x2A\x00\x00\x00\x08")
	tiff = append(tiff, 0, 2)                                                           // two entries
	tiff = append(tiff, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, byte(o), 0, 0)                 // orientation
	tiff = append(tiff, 0x01, 0x0F, 0, 2, 0, 0, 0, byte(len(gpsMarker)+1), 0, 0, 0, 38) // make -> offset 38
	tiff = append(tiff, 0, 0, 0, 0)
	tiff = append(tiff, gpsMarker...)
	tiff = append(tiff, 0)
	p := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(p)+2))
	return append(seg, p...)
}

func comSegment(s string) []byte {
	seg := []byte{0xFF, 0xFE, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(s)+2))
	return append(seg, s...)
}

func jpegWithMetadata(t *testing.T, orientation int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, testImage(), nil); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	out := append([]byte{0xFF, 0xD8}, exifSegment(orientation)...)
	out = append(out, comSegment("camera-serial-12345")...)
	return append(out, raw[2:]...)
}

func pngWithMetadata(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, testImage()); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	chunk := func(typ, data string) []byte {
		c := make([]byte, 8, 12+len(data))
		binary.BigEndian.PutUint32(c, uint32(len(data)))
		copy(c[4:], typ)
		c = append(c, data...)
		return append(c, 0, 0, 0, 0) // CRC not checked by us; the decoder is not asked to read this chunk
	}
	// insert tEXt + eXIf right after IHDR (8 sig + 25 IHDR)
	out := append([]byte{}, raw[:33]...)
	out = append(out, chunk("tEXt", "Comment\x00"+gpsMarker)...)
	out = append(out, chunk("eXIf", gpsMarker)...)
	out = append(out, raw[33:]...)
	return append(out, "TRAILING-POLYGLOT-DATA"...)
}

func gifWithComment(t *testing.T) []byte {
	t.Helper()
	pal := image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black, color.White})
	var b bytes.Buffer
	if err := gif.Encode(&b, pal, nil); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	// header(6)+LSD(7)+global palette(2*3) then blocks; insert a comment extension there.
	at := 6 + 7 + 6
	com := append([]byte{0x21, 0xFE, byte(len(gpsMarker))}, gpsMarker...)
	com = append(com, 0)
	return append(append(append([]byte{}, raw[:at]...), com...), raw[at:]...)
}

// webpWithMetadata wraps the well-known 1x1 lossless WebP in a VP8X container with EXIF.
func webpWithMetadata(t *testing.T) []byte {
	t.Helper()
	vp8l, _ := hexDecode("2f0000001007101111888 8fe0700")
	chunk := func(tag string, data []byte) []byte {
		c := append([]byte(tag), 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(c[4:], uint32(len(data)))
		c = append(c, data...)
		if len(data)&1 == 1 {
			c = append(c, 0)
		}
		return c
	}
	vp8x := []byte{0x08 | 0x20, 0, 0, 0, 0, 0, 0, 0, 0, 0} // EXIF + ICC flags, canvas 1x1
	body := []byte("WEBP")
	body = append(body, chunk("VP8X", vp8x)...)
	body = append(body, chunk("EXIF", []byte("Exif\x00\x00"+gpsMarker))...)
	body = append(body, chunk("VP8L", vp8l)...)
	out := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
	return append(out, body...)
}

func hexDecode(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, " ", "")
	b := make([]byte, len(s)/2)
	_, err := fmt.Sscanf(s, "%x", &b)
	return b, err
}

func TestSniff(t *testing.T) {
	for name, tc := range map[string]struct {
		in   []byte
		want imgKind
		ok   bool
	}{
		"jpeg":  {[]byte{0xFF, 0xD8, 0xFF, 0xE0}, kJPEG, true},
		"png":   {[]byte("\x89PNG\r\n\x1a\nxxxx"), kPNG, true},
		"gif":   {[]byte("GIF89a...."), kGIF, true},
		"webp":  {[]byte("RIFF\x00\x00\x00\x00WEBPVP8L"), kWEBP, true},
		"svg":   {[]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`), imgKind{}, false},
		"html":  {[]byte("<html><script>alert(1)</script>"), imgKind{}, false},
		"exe":   {[]byte("MZ\x90\x00"), imgKind{}, false},
		"empty": {nil, imgKind{}, false},
	} {
		got, ok := sniff(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: sniff = %v,%v", name, got, ok)
		}
	}
}

func decodeJPEGPixels(t *testing.T, b []byte) image.Image {
	t.Helper()
	im, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("stripped JPEG does not decode: %v", err)
	}
	return im
}

func TestStripJPEG(t *testing.T) {
	in := jpegWithMetadata(t, 6)
	if !bytes.Contains(in, []byte(gpsMarker)) {
		t.Fatal("fixture has no metadata")
	}
	out, err := strip(kJPEG, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{gpsMarker, "camera-serial", "Make"} {
		if bytes.Contains(out, []byte(leak)) {
			t.Fatalf("metadata %q survived", leak)
		}
	}
	// Orientation is preserved so rotated photos still display upright.
	i := bytes.Index(out, []byte("Exif\x00\x00"))
	if i < 0 {
		t.Fatal("orientation segment missing")
	}
	if o := exifOrientation(out[i:]); o != 6 {
		t.Fatalf("orientation = %d, want 6", o)
	}
	// Pixel data is untouched (no re-encode).
	a, b := decodeJPEGPixels(t, in), decodeJPEGPixels(t, out)
	if a.Bounds() != b.Bounds() || a.At(3, 2) != b.At(3, 2) {
		t.Fatal("pixels changed")
	}
	// Orientation 1 (normal) needs no EXIF at all.
	out, err = strip(kJPEG, jpegWithMetadata(t, 1))
	if err != nil || bytes.Contains(out, []byte("Exif")) {
		t.Fatalf("normal orientation should leave no EXIF (err=%v)", err)
	}
}

func TestStripPNG(t *testing.T) {
	in := pngWithMetadata(t)
	out, err := strip(kPNG, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{gpsMarker, "tEXt", "eXIf", "TRAILING"} {
		if bytes.Contains(out, []byte(leak)) {
			t.Fatalf("%q survived", leak)
		}
	}
	got, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds() != testImage().Bounds() {
		t.Fatal("size changed")
	}
}

func TestStripGIF(t *testing.T) {
	in := gifWithComment(t)
	out, err := strip(kGIF, in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(gpsMarker)) {
		t.Fatal("comment survived")
	}
	if _, err := gif.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("stripped GIF does not decode: %v", err)
	}
}

func TestStripWEBP(t *testing.T) {
	in := webpWithMetadata(t)
	if _, err := webp.DecodeConfig(bytes.NewReader(in)); err != nil {
		t.Skipf("fixture is not a valid WebP for this decoder: %v", err)
	}
	out, err := strip(kWEBP, in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(gpsMarker)) || bytes.Contains(out, []byte("EXIF")) {
		t.Fatal("EXIF survived")
	}
	if got := binary.LittleEndian.Uint32(out[4:]); int(got) != len(out)-8 {
		t.Fatalf("RIFF size %d, want %d", got, len(out)-8)
	}
	if i := bytes.Index(out, []byte("VP8X")); out[i+8]&0x08 != 0 || out[i+8]&0x20 == 0 {
		t.Fatalf("VP8X flags wrong: %#x (EXIF must be cleared, ICC kept)", out[i+8])
	}
	if _, err := webp.DecodeConfig(bytes.NewReader(out)); err != nil {
		t.Fatalf("stripped WebP does not decode: %v", err)
	}
}

// Truncating or corrupting an image must give an error or a valid result, never a panic.
func TestStripNeverPanicsOnDamagedInput(t *testing.T) {
	samples := map[imgKind][]byte{
		kJPEG: jpegWithMetadata(t, 3), kPNG: pngWithMetadata(t), kGIF: gifWithComment(t), kWEBP: webpWithMetadata(t),
	}
	for k, s := range samples {
		for n := 0; n < len(s); n++ {
			_, _ = strip(k, s[:n])
		}
		for i := 0; i < len(s); i++ {
			c := append([]byte(nil), s...)
			c[i] ^= 0xFF
			_, _ = strip(k, c)
		}
	}
	if _, err := strip(kJPEG, []byte{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF}); err == nil {
		t.Fatal("oversized segment length accepted")
	}
}

// ---- HTTP ------------------------------------------------------------------

type fakeIdentity struct{}

func (fakeIdentity) Manifest() sdk.Manifest {
	return sdk.Manifest{ID: "fakeid", Name: "fake", Version: "0", APIVersion: sdk.APIVersion,
		Capabilities: []sdk.Capability{sdk.CapIdentity}, Provides: []string{sdk.ServiceIdentity}}
}
func (f fakeIdentity) Init(_ context.Context, h sdk.Host) error {
	return h.Provide(sdk.ServiceIdentity, f)
}
func (fakeIdentity) Authenticate(r *http.Request) (*sdk.Principal, error) {
	var role string
	var id int64
	v := r.Header.Get("X-Test-User")
	if v == "" {
		return nil, nil
	}
	if _, err := fmt.Sscanf(strings.ReplaceAll(v, ":", " "), "%s %d", &role, &id); err != nil {
		return nil, err
	}
	perms := map[string]bool{permRead: true, permWrite: true}
	if role == "editor" {
		perms[permManage] = true
	}
	if role == "reader" {
		perms = map[string]bool{permRead: true}
	}
	return &sdk.Principal{UserID: id, Username: "u", Roles: []string{role}, Perms: perms}, nil
}

type env struct {
	t      *testing.T
	h      http.Handler
	dir    string
	plugin *Plugin
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, nil)
}

func newEnvWith(t *testing.T, tweak func(*Plugin)) *env {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	dir := t.TempDir()
	m := plugins.New(slog.New(slog.NewTextHandler(io.Discard, nil)), d)
	m.SetDataDir(dir)
	m.Register(fakeIdentity{})
	pl := New()
	if tweak != nil {
		tweak(pl)
	}
	m.Register(pl)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", m.APIHandler())
	mux.Handle("/", m.PublicHandler())
	return &env{t: t, h: m.IdentityMiddleware(mux), dir: dir, plugin: pl}
}

func (e *env) req(user, method, path string, body io.Reader, ctype string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://cms.test"+path, body)
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	if user != "" {
		r.Header.Set("X-Test-User", user)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *env) upload(user, filename string, data []byte) *httptest.ResponseRecorder {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	_ = mw.WriteField("note", "ignored field")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	h.Set("Content-Type", "image/png") // a lying client header must not matter
	pw, _ := mw.CreatePart(h)
	_, _ = pw.Write(data)
	_ = mw.Close()
	return e.req(user, "POST", "/api/v1/media/items", &b, mw.FormDataContentType())
}

func want(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status = %d, want %d; body: %.300s", w.Code, code, w.Body.String())
	}
}

func decodeItem(t *testing.T, w *httptest.ResponseRecorder) itemView {
	t.Helper()
	var it itemView
	if err := json.Unmarshal(w.Body.Bytes(), &it); err != nil {
		t.Fatal(err)
	}
	return it
}

func TestUploadServeDelete(t *testing.T) {
	e := newEnv(t)
	want(t, e.upload("", "a.jpg", jpegWithMetadata(t, 1)), 401)
	want(t, e.upload("reader:9", "a.jpg", jpegWithMetadata(t, 1)), 403)

	w := e.upload("author:3", "../../etc/passwd photo.jpg", jpegWithMetadata(t, 1))
	want(t, w, 201)
	it := decodeItem(t, w)
	if it.Mime != "image/jpeg" || it.Width != 8 || it.Height != 6 || !strings.HasPrefix(it.URL, "/media/") {
		t.Fatalf("item: %+v", it)
	}
	if strings.ContainsAny(it.Filename, "/\\\x00") || it.Filename != "passwd photo.jpg" {
		t.Fatalf("filename not sanitized: %q", it.Filename)
	}

	got := e.req("", "GET", it.URL, nil, "") // media is public
	want(t, got, 200)
	if got.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(got.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("headers: %v", got.Header())
	}
	if bytes.Contains(got.Body.Bytes(), []byte(gpsMarker)) {
		t.Fatal("served file still carries metadata")
	}
	if int64(got.Body.Len()) != it.Size {
		t.Fatalf("size mismatch %d vs %d", got.Body.Len(), it.Size)
	}
	// Range requests work (video-less, but browsers use them for large images).
	r := httptest.NewRequest("GET", "http://cms.test"+it.URL, nil)
	r.Header.Set("Range", "bytes=0-3")
	rw := httptest.NewRecorder()
	e.h.ServeHTTP(rw, r)
	want(t, rw, 206)

	// Files are on disk under the plugin's private directory, with private permissions.
	files, _ := filepath.Glob(filepath.Join(e.dir, "plugins", "media", "*"))
	if len(files) != 1 {
		t.Fatalf("files on disk: %v", files)
	}

	// Others cannot delete; the uploader and managers can.
	path := fmt.Sprintf("/api/v1/media/items/%d", it.ID)
	want(t, e.req("author:4", "DELETE", path, nil, ""), 403)
	want(t, e.req("", "DELETE", path, nil, ""), 401)
	want(t, e.req("author:3", "DELETE", path, nil, ""), 204)
	want(t, e.req("", "GET", it.URL, nil, ""), 404)
	want(t, e.req("author:3", "DELETE", path, nil, ""), 404)

	again := decodeItem(t, e.upload("author:3", "b.png", pngWithMetadata(t)))
	want(t, e.req("editor:1", "DELETE", fmt.Sprintf("/api/v1/media/items/%d", again.ID), nil, ""), 204)
}

func TestUploadRejections(t *testing.T) {
	e := newEnv(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	want(t, e.upload("author:3", "x.png", svg), 415)
	want(t, e.upload("author:3", "x.png", []byte("<html>")), 415)
	want(t, e.upload("author:3", "x.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 4, 0, 0}), 400) // truncated
	want(t, e.upload("author:3", "x.jpg", append(jpegWithMetadata(t, 1), make([]byte, maxUpload)...)), 413)

	// Pixel bomb: a PNG header declaring an enormous canvas must be refused before anyone decodes it.
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 4)))
	bomb := b.Bytes()
	binary.BigEndian.PutUint32(bomb[16:], 60000)
	binary.BigEndian.PutUint32(bomb[20:], 60000)
	want(t, e.upload("author:3", "bomb.png", bomb), 400)

	// Not multipart / no file field.
	want(t, e.req("author:3", "POST", "/api/v1/media/items", strings.NewReader("x"), "application/json"), 400)
	var mb bytes.Buffer
	mw := multipart.NewWriter(&mb)
	_ = mw.WriteField("other", "x")
	_ = mw.Close()
	want(t, e.req("author:3", "POST", "/api/v1/media/items", &mb, mw.FormDataContentType()), 400)

	if files, _ := filepath.Glob(filepath.Join(e.dir, "plugins", "media", "*")); len(files) != 0 {
		t.Fatalf("rejected uploads left files behind: %v", files)
	}
}

func TestServeRejectsOddNames(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"..%2f..%2fetc%2fpasswd", "x.jpg", "ABCDEF0123456789ABCDEF0123456789.jpg", strings.Repeat("a", 32) + ".svg", strings.Repeat("a", 32) + ".jpg/../x"} {
		if w := e.req("", "GET", "/media/"+n, nil, ""); w.Code != 404 && w.Code != 307 { // 307: the mux redirects to the cleaned path, which is then a 404
			t.Errorf("%s: status %d", n, w.Code)
		}
	}
}

func TestListMedia(t *testing.T) {
	e := newEnv(t)
	want(t, e.req("", "GET", "/api/v1/media/items", nil, ""), 401)
	for i := 0; i < 3; i++ {
		want(t, e.upload("author:3", fmt.Sprintf("f%d.png", i), pngWithMetadata(t)), 201)
	}
	w := e.req("reader:7", "GET", "/api/v1/media/items?limit=2", nil, "")
	want(t, w, 200)
	var items []itemView
	_ = json.Unmarshal(w.Body.Bytes(), &items)
	if len(items) != 2 || items[0].Filename != "f2.png" {
		t.Fatalf("list: %+v", items)
	}
}

func TestCleanFilename(t *testing.T) {
	for in, out := range map[string]string{
		"photo.jpg":              "photo.jpg",
		"C:\\Users\\me\\a.png":   "a.png",
		"a\x00b\x07c.png":        "abc.png",
		"":                       "image",
		"   ":                    "image",
		"/":                      "image",
		strings.Repeat("я", 300): strings.Repeat("я", 100),
	} {
		if got := cleanFilename(in); got != out {
			t.Errorf("cleanFilename(%q) = %q, want %q", in, got, out)
		}
	}
}

// Phones append further JPEGs (MPF: depth map, gain map, thumbnail) after the
// main image, each with its own EXIF and GPS. The output must end at the first EOI.
func TestStripJPEGDropsEverythingAfterEOI(t *testing.T) {
	main := jpegWithMetadata(t, 1)
	second := jpegWithMetadata(t, 1)
	out, err := strip(kJPEG, append(append(append([]byte{}, main...), second...), "TRAILING-TEXT"...))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(gpsMarker)) || bytes.Contains(out, []byte("TRAILING")) {
		t.Fatal("data after the first EOI survived")
	}
	if !bytes.HasSuffix(out, []byte{0xFF, 0xD9}) || bytes.Count(out, []byte{0xFF, 0xD8}) != 1 {
		t.Fatal("output must be exactly one image ending at EOI")
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Fatal(err)
	}
	// Appended data after a valid image, with no second image at all.
	out, err = strip(kJPEG, append(append([]byte{}, main...), make([]byte, 1000)...))
	if err != nil || !bytes.HasSuffix(out, []byte{0xFF, 0xD9}) {
		t.Fatalf("zero padding: %v", err)
	}
}

// Multi-scan (progressive) layout: tables and SOS between scans, restart
// markers and stuffed FF 00 bytes inside entropy-coded data.
func TestStripJPEGScanStructure(t *testing.T) {
	seg := func(m byte, payload string) []byte {
		s := []byte{0xFF, m, 0, byte(len(payload) + 2)}
		return append(s, payload...)
	}
	cat := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	scan1 := []byte{0x12, 0xFF, 0x00, 0x34, 0xFF, 0xD0, 0x56, 0xFF, 0xFF, 0x00, 0x78}
	in := cat([]byte{0xFF, 0xD8}, seg(0xE0, "JFIF\x00"), seg(0xFE, "comment"), seg(0xDB, "tables"),
		seg(0xDA, "scan-header-1"), scan1, seg(0xC4, "more-tables"), seg(0xDA, "scan-header-2"), []byte{0x9A, 0xBC},
		[]byte{0xFF, 0xD9}, []byte("GARBAGE"), seg(0xE1, "Exif\x00\x00"+gpsMarker))
	want := cat([]byte{0xFF, 0xD8}, seg(0xE0, "JFIF\x00"), seg(0xDB, "tables"),
		seg(0xDA, "scan-header-1"), scan1, seg(0xC4, "more-tables"), seg(0xDA, "scan-header-2"), []byte{0x9A, 0xBC},
		[]byte{0xFF, 0xD9})
	got, err := stripJPEG(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got  %x\nwant %x", got, want)
	}
	// No EOI, or no scan at all: rejected.
	if _, err := stripJPEG(in[:len(in)-len("GARBAGE")-len(seg(0xE1, "Exif\x00\x00"+gpsMarker))-2]); err == nil {
		t.Fatal("JPEG without EOI accepted")
	}
	if _, err := stripJPEG(cat([]byte{0xFF, 0xD8}, seg(0xDB, "t"), []byte{0xFF, 0xD9})); err == nil {
		t.Fatal("JPEG without a scan accepted")
	}
}

func TestUploadQuota(t *testing.T) {
	img := pngWithMetadata(t)
	clean, _ := strip(kPNG, img)
	e := newEnvWith(t, func(p *Plugin) { p.quota = int64(len(clean)*2 + 10) })
	want(t, e.upload("author:3", "1.png", img), 201)
	want(t, e.upload("author:3", "2.png", img), 201)
	want(t, e.upload("author:3", "3.png", img), 413) // over quota
	want(t, e.upload("author:4", "4.png", img), 201) // quotas are per uploader
	// Deleting frees quota.
	var items []itemView
	w := e.req("author:3", "GET", "/api/v1/media/items", nil, "")
	_ = json.Unmarshal(w.Body.Bytes(), &items)
	var mine int64
	for _, it := range items {
		if it.UploaderID == 3 {
			mine = it.ID
		}
	}
	want(t, e.req("author:3", "DELETE", fmt.Sprintf("/api/v1/media/items/%d", mine), nil, ""), 204)
	want(t, e.upload("author:3", "5.png", img), 201)
}

// The upload route must outlive the server-wide read timeout (slow mobile links).
func TestUploadSurvivesShortServerReadTimeout(t *testing.T) {
	uploadSlowly(t, func(c *http.Server) { c.ReadTimeout = 400 * time.Millisecond })
}

// The server-wide WriteTimeout counts from the moment the request headers were
// read, so a slow upload would finish reading and then find the response write
// already timed out: the file is stored, the client sees a broken connection and
// uploads again. The route must extend the write deadline as well.
func TestUploadSurvivesShortServerWriteTimeout(t *testing.T) {
	uploadSlowly(t, func(c *http.Server) { c.WriteTimeout = 400 * time.Millisecond })
}

// uploadSlowly sends an image whose second half arrives 900 ms late to a real
// server configured by tweak, and requires a normal 201 answer.
func uploadSlowly(t *testing.T, tweak func(*http.Server)) {
	t.Helper()
	e := newEnv(t)
	srv := httptest.NewUnstartedServer(e.h)
	tweak(srv.Config)
	srv.Start()
	defer srv.Close()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="a.png"`)
	pw, _ := mw.CreatePart(h)
	_, _ = pw.Write(pngWithMetadata(t))
	_ = mw.Close()
	data := body.Bytes()

	pr, pwr := io.Pipe()
	go func() {
		_, _ = pwr.Write(data[:len(data)/2])
		time.Sleep(900 * time.Millisecond) // slower than ReadTimeout
		_, _ = pwr.Write(data[len(data)/2:])
		_ = pwr.Close()
	}()
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/media/items", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Test-User", "author:3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("slow upload failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
}

// The first image of a page is usually the one the visitor sees at once: lazy loading it
// delays the largest paint, so only the images after it are lazy.
func TestHTMLFilterAddsDimensionsAndLazyLoadsAllButTheFirst(t *testing.T) {
	e := newEnv(t)
	it := decodeItem(t, e.upload("author:3", "a.png", pngWithMetadata(t))) // 8x6
	in := `<p>x</p><img src="` + it.URL + `" alt="a"><img src="/media/` + strings.Repeat("0", 32) + `.png" alt="unknown"><img src="https://other.example/x.png" alt="ext"><img src="` + it.URL + `" alt="b">`
	got, err := e.plugin.filterHTML(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	out := got.(string)
	if !strings.Contains(out, `<img src="`+it.URL+`" alt="a" width="8" height="6" decoding="async">`) {
		t.Errorf("the first image must not be lazy: %s", out)
	}
	if !strings.Contains(out, `<img src="`+it.URL+`" alt="b" width="8" height="6" loading="lazy" decoding="async">`) {
		t.Errorf("later images must be lazy: %s", out)
	}
	if strings.Count(out, "width=") != 2 {
		t.Errorf("unknown and external images must be left alone: %s", out)
	}
	// Idempotent: running the filter again changes nothing.
	again, _ := e.plugin.filterHTML(context.Background(), out)
	if again.(string) != out {
		t.Errorf("filter is not idempotent: %s", again)
	}
	if v, _ := e.plugin.filterHTML(context.Background(), 42); v != 42 {
		t.Error("non-string values must pass through")
	}
}

// Unknown names are remembered briefly (no DB query per view), then retried.
func TestDimensionsNegativeCacheExpires(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	e := newEnvWith(t, func(p *Plugin) { p.now = func() time.Time { return now } })
	name := strings.Repeat("0", 32) + ".png"
	if _, ok := e.plugin.dimensions(context.Background(), name); ok {
		t.Fatal("unknown name reported as known")
	}
	if d, ok := e.plugin.dc.m[name]; !ok || d.miss.IsZero() {
		t.Fatal("miss was not remembered")
	}
	now = now.Add(missTTL + time.Second)
	if _, ok := e.plugin.dimensions(context.Background(), name); ok {
		t.Fatal("a name that is still missing must stay unknown after expiry")
	}
	if d := e.plugin.dc.m[name]; !d.miss.After(now) {
		t.Fatal("expired miss was not refreshed by a new lookup")
	}
}
