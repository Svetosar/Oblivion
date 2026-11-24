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
	t   *testing.T
	h   http.Handler
	dir string
}

func newEnv(t *testing.T) *env {
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
	m.Register(New())
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", m.APIHandler())
	mux.Handle("/", m.PublicHandler())
	return &env{t: t, h: m.IdentityMiddleware(mux), dir: dir}
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
