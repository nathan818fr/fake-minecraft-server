package status

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"os"
	"strings"
)

// FaviconPrefix starts the favicons that clients show: data URIs of PNG images.
// Clients ignore the other formats.
const FaviconPrefix = "data:image/png;base64,"

// faviconSize is the width and height of the favicons that clients show: they
// ignore the other sizes.
const faviconSize = 64

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// ReadFavicon reads the PNG file at path, a 64×64 image, as a favicon.
func ReadFavicon(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := checkFaviconPNG(data); err != nil {
		return "", fmt.Errorf("%s %w", path, err)
	}
	return FaviconPrefix + base64.StdEncoding.EncodeToString(data), nil
}

// CheckFavicon checks that uri is a favicon that clients show: a data URI of a
// 64×64 PNG image.
func CheckFavicon(uri string) error {
	encoded, ok := strings.CutPrefix(uri, FaviconPrefix)
	if !ok {
		return fmt.Errorf("must start with %q", FaviconPrefix)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return errors.New("is not valid base64")
	}
	return checkFaviconPNG(data)
}

// checkFaviconPNG checks that data starts like a 64×64 PNG image.
func checkFaviconPNG(data []byte) error {
	if !bytes.HasPrefix(data, pngSignature) {
		return errors.New("is not a PNG image")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("is not a valid PNG image: %w", err)
	}
	if cfg.Width != faviconSize || cfg.Height != faviconSize {
		return fmt.Errorf("is %d×%d pixels, clients only show %d×%d favicons", cfg.Width, cfg.Height, faviconSize, faviconSize)
	}
	return nil
}
