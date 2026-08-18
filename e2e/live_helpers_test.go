//go:build live

package e2e

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ki/internal/server"
)

const pdfMarker = "KI-PDF-MARKER-42"

func writeRedPNG(t *testing.T, path string) {
	t.Helper()
	writeSolidPNG(t, path, color.RGBA{R: 200, G: 16, B: 16, A: 255})
}

func writeBluePNG(t *testing.T, path string) {
	t.Helper()
	writeSolidPNG(t, path, color.RGBA{R: 16, G: 16, B: 200, A: 255})
}

func writeSolidPNG(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			img.SetRGBA(x, y, c)
		}
	}
	//nolint:gosec // path is a live-test fixture under t.TempDir.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func writeMarkerPDF(t *testing.T, path, marker string) {
	t.Helper()
	stream := fmt.Sprintf("BT /F1 18 Tf 20 60 Td (%s) Tj ET\n", marker)
	pdf := fmt.Sprintf("%%PDF-1.1\n"+
		"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n"+
		"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n"+
		"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 300 144]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj\n"+
		"4 0 obj<</Length %d>>stream\n%s\nendstream\nendobj\n"+
		"5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj\n"+
		"trailer<</Root 1 0 R>>\n%%%%EOF\n", len(stream), stream)
	if err := os.WriteFile(path, []byte(pdf), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeHomeTOML(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "ki.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func liveChildEnv(home string) []string {
	env := os.Environ()
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, "KI_HOME=") || strings.HasPrefix(e, "KI_FAKE=") || strings.HasPrefix(e, "KI_SERVER_ADDR=") {
			continue
		}
		out = append(out, e)
	}
	out = append(out, "KI_HOME="+home, "KI_FAKE=")
	return out
}

func startServeLive(t *testing.T, home, dir string) server.File {
	t.Helper()
	return startServeEnv(t, home, dir, liveChildEnv(home))
}
