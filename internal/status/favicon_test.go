package status

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color/palette"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	return encodeImage(t, image.NewGray(image.Rect(0, 0, width, height)))
}

func encodeImage(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReadFavicon(t *testing.T) {
	dir := t.TempDir()
	icon := encodePNG(t, 64, 64)
	files := map[string][]byte{"icon.png": icon, "large.png": encodePNG(t, 128, 64), "text.png": []byte("hello")}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if uri, err := ReadFavicon(filepath.Join(dir, "icon.png")); err != nil || uri != FaviconPrefix+base64.StdEncoding.EncodeToString(icon) {
		t.Errorf("ReadFavicon() = %.40s, %v, want the data URI of the file", uri, err)
	}
	for name, want := range map[string]string{
		"large.png": " is 128×64 pixels, clients only show 64×64 favicons",
		"text.png":  " is not a PNG image",
	} {
		path := filepath.Join(dir, name)
		if _, err := ReadFavicon(path); err == nil || err.Error() != path+want {
			t.Errorf("ReadFavicon(%s) error = %v, want %s", name, err, path+want)
		}
	}
}

func TestCheckFavicon(t *testing.T) {
	icon := base64.StdEncoding.EncodeToString(encodePNG(t, 64, 64))
	tests := []struct{ uri, want string }{
		{FaviconPrefix + icon, ""},
		// Palette images are read up to their PLTE chunk, after the header.
		{FaviconPrefix + base64.StdEncoding.EncodeToString(encodeImage(t, image.NewPaletted(image.Rect(0, 0, 64, 64), palette.Plan9))), ""},
		{FaviconPrefix + icon[:44] + "!", "is not valid base64"},
		{"data:image/jpeg;base64," + icon, `must start with "data:image/png;base64,"`},
		{FaviconPrefix + "!!!!", "is not valid base64"},
		{FaviconPrefix + "aGVsbG8=", "is not a PNG image"},
		{FaviconPrefix + base64.StdEncoding.EncodeToString(encodePNG(t, 16, 16)), "is 16×16 pixels, clients only show 64×64 favicons"},
	}
	for _, tc := range tests {
		got := ""
		if err := CheckFavicon(tc.uri); err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("CheckFavicon(%.40s) error = %q, want %q", tc.uri, got, tc.want)
		}
	}
}
