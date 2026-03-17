package api

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"

	"github.com/skip2/go-qrcode"
)

// generateQRCode creates a PNG-encoded QR code from the given text.
func generateQRCode(text string) ([]byte, error) {
	qr, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return nil, fmt.Errorf("qrcode encode: %w", err)
	}
	qr.DisableBorder = false
	qr.ForegroundColor = color.Black
	qr.BackgroundColor = color.White

	img := qr.Image(512)
	return encodePNG(img)
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
