package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type packageSpec struct {
	name  string
	files map[string]string
}

func main() {
	out := flag.String("out", "", "Release directory to create")
	release := flag.String("release", "v0.0.0-test", "Release tag (one directory level)")
	flag.Parse()
	if *out == "" {
		fail(errors.New("-out is required"))
	}
	target := filepath.Join(*out, *release)
	if err := os.MkdirAll(target, 0o755); err != nil {
		fail(err)
	}
	packages := []packageSpec{
		{
			name: "goal",
			files: map[string]string{
				"extension.json":   `{"name":"goal","version":"0.1.0","description":"Fixture extension installed from the test mirror.","capabilities":["prompt.append"],"prompt":{"append":["prompt/APPEND.md"]}}`,
				"prompt/APPEND.md": "fixture install prompt",
			},
		},
		{
			name: "telegram-bot",
			files: map[string]string{
				"extension.json": `{"name":"telegram-bot","version":"0.1.0","description":"Fixture channel extension.","capabilities":["settings"],"config":{"schema":{"type":"object","properties":{"token":{"type":"string"}}}}}`,
			},
		},
	}
	sums := make([]string, 0, len(packages))
	for _, pkg := range packages {
		asset := fmt.Sprintf("%s-%s-%s.zip", pkg.name, runtime.GOOS, runtime.GOARCH)
		if err := writeZip(filepath.Join(target, asset), pkg); err != nil {
			fail(err)
		}
		digest, err := sha256File(filepath.Join(target, asset))
		if err != nil {
			fail(err)
		}
		sums = append(sums, digest+"  "+asset)
	}
	sort.Strings(sums)
	if err := os.WriteFile(filepath.Join(target, "checksums.txt"), []byte(strings.Join(sums, "\n")+"\n"), 0o644); err != nil {
		fail(err)
	}
	fmt.Println(target)
}

func writeZip(destination string, pkg packageSpec) error {
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(output)
	names := make([]string, 0, len(pkg.files))
	for name := range pkg.files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: pkg.name + "/" + name, Method: zip.Deflate}
		writer, err := archive.CreateHeader(header)
		if err != nil {
			_ = archive.Close()
			_ = output.Close()
			return err
		}
		if _, err := io.WriteString(writer, pkg.files[name]); err != nil {
			_ = archive.Close()
			_ = output.Close()
			return err
		}
	}
	if err := archive.Close(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path) //nolint:gosec // fixture path built by this command
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
