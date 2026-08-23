package tools

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	// Register WebP with the standard image decoder registry.
	_ "golang.org/x/image/webp"
	_ "image/gif"
)

const (
	maxImageDimension = 2000
	maxImageBytes     = 4_500_000
)

type imageDetails struct {
	OriginalWidth  int    `json:"original_width"`
	OriginalHeight int    `json:"original_height"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	OriginalBytes  int    `json:"original_bytes"`
	Bytes          int    `json:"bytes"`
	MIMEType       string `json:"mime_type"`
	Resized        bool   `json:"resized"`
}

func resizeImageForModel(data []byte, mime string) ([]byte, string, *imageDetails, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", nil, fmt.Errorf("decode image config: %w", err)
	}
	details := &imageDetails{OriginalWidth: cfg.Width, OriginalHeight: cfg.Height, Width: cfg.Width, Height: cfg.Height, OriginalBytes: len(data), Bytes: len(data), MIMEType: mime}
	if cfg.Width <= maxImageDimension && cfg.Height <= maxImageDimension && len(data) <= maxImageBytes {
		return data, mime, details, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", nil, fmt.Errorf("decode image: %w", err)
	}
	w, h := boundedDimensions(cfg.Width, cfg.Height, maxImageDimension)
	for {
		dst := scaleImage(src, w, h)
		encoded, outMIME, encErr := encodeModelImage(dst, hasTransparency(src))
		if encErr != nil {
			return nil, "", nil, encErr
		}
		if len(encoded) <= maxImageBytes || w <= 1 || h <= 1 {
			if len(encoded) > maxImageBytes {
				return nil, "", nil, fmt.Errorf("%w %d bytes after resizing", errImageStillTooLarge, maxImageBytes)
			}
			details.Width, details.Height, details.Bytes, details.MIMEType, details.Resized = w, h, len(encoded), outMIME, true
			return encoded, outMIME, details, nil
		}
		w, h = max(1, w*3/4), max(1, h*3/4)
	}
}

func boundedDimensions(w, h, limit int) (int, int) {
	if w <= limit && h <= limit {
		return w, h
	}
	if w >= h {
		return limit, max(1, h*limit/w)
	}
	return max(1, w*limit/h), limit
}

// Nearest-neighbor resampling keeps this path dependency-light and works for every
// image format decoded by the standard image interfaces.
func scaleImage(src image.Image, width, height int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	b := src.Bounds()
	for y := range height {
		sy := b.Min.Y + y*b.Dy()/height
		for x := range width {
			sx := b.Min.X + x*b.Dx()/width
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

func hasTransparency(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a != 0xffff {
				return true
			}
		}
	}
	return false
}

func encodeModelImage(img image.Image, transparent bool) ([]byte, string, error) {
	var out bytes.Buffer
	if transparent {
		err := png.Encode(&out, img)
		return out.Bytes(), "image/png", err
	}
	// Flatten any non-opaque color implementation before JPEG encoding.
	flat := image.NewRGBA(img.Bounds())
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			flat.Set(x, y, color.NRGBAModel.Convert(img.At(x, y)))
		}
	}
	err := jpeg.Encode(&out, flat, &jpeg.Options{Quality: 80})
	return out.Bytes(), "image/jpeg", err
}
