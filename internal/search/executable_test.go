package search

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMaterializeToolsWritesBinariesAndShim(t *testing.T) {
	dir := t.TempDir()
	if err := materializeTools(dir); err != nil {
		t.Fatal(err)
	}

	binaries := embeddedBinaries()
	if len(binaries) == 0 {
		t.Skip("no embedded executables for this platform")
	}
	for _, bin := range binaries {
		info, err := os.Stat(filepath.Join(dir, bin.name))
		if err != nil {
			t.Fatalf("%s: %v", bin.name, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("%s is not executable: %v", bin.name, info.Mode())
		}
	}

	shim, err := os.ReadFile(filepath.Join(dir, ToolsShimName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shim), "PATH=") || !strings.Contains(string(shim), "KI_ORIG_BASH_ENV") {
		t.Fatalf("shim does not adjust PATH / chain BASH_ENV:\n%s", shim)
	}
}

func TestMaterializeToolsReusesMatchingBinaries(t *testing.T) {
	dir := t.TempDir()
	if err := materializeTools(dir); err != nil {
		t.Fatal(err)
	}
	binaries := embeddedBinaries()
	if len(binaries) == 0 {
		t.Skip("no embedded executables for this platform")
	}
	path := filepath.Join(dir, binaries[0].name)

	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeTools(dir); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ModTime().Equal(second.ModTime()) {
		t.Fatalf("matching binary was rewritten: %v -> %v", first.ModTime(), second.ModTime())
	}
}

func TestToolsDirExposesEmbeddedExecutables(t *testing.T) {
	dir, err := ToolsDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rg", "fd"} {
		_, data := embeddedRG()
		if want == "rg" && len(data) == 0 {
			continue
		}
		if want == "fd" {
			if _, fdData := embeddedFD(); len(fdData) == 0 {
				continue
			}
		}
		if _, err := os.Stat(filepath.Join(dir, executableName(want))); err != nil {
			t.Errorf("%s missing from ToolsDir: %v", want, err)
		}
	}
}
