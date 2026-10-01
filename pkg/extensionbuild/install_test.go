package extensionbuild

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func sourceFixture(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.25.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestInstallBuildsNativeExecutableOnceAcrossConcurrentLaunches(t *testing.T) {
	root := sourceFixture(t, `package main
import("fmt";"runtime";"runtime/debug")
func main(){info,_:=debug.ReadBuildInfo();for _,setting:=range info.Settings{if setting.Key=="CGO_ENABLED"{fmt.Printf("%s/%s cgo=%s",runtime.GOOS,runtime.GOARCH,setting.Value)}}}`)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- Install(t.Context(), root, "fixture", io.Discard) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "bin", executable("fixture"))
	data, err := exec.Command(binary).CombinedOutput()
	if err != nil || !strings.HasSuffix(string(data), "cgo=0") {
		t.Fatal(string(data), err)
	}
	before, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if err := Install(context.Background(), root, "fixture", io.Discard); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(binary)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("rebuilt an existing executable")
	}
}
func TestInstallReportsMissingSourcesAndToolchain(t *testing.T) {
	for _, name := range []string{"goal", "zvec-grep"} {
		err := Install(t.Context(), t.TempDir(), name, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "sources are missing") {
			t.Fatal(name, err)
		}
	}
	root := sourceFixture(t, "package main\nfunc main(){}\n")
	t.Setenv("PATH", t.TempDir())
	err := Install(t.Context(), root, "goal", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "requires Go") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "bin", executable("goal"))); !os.IsNotExist(err) {
		t.Fatal("published failed build", err)
	}
}
func TestFailedBuildPreservesExistingFileAndCleansTemporaryOutput(t *testing.T) {
	root := sourceFixture(t, "package main\ninvalid source\n")
	binary := filepath.Join(root, "bin", executable("fixture"))
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		t.Fatal(err)
	}
	// A non-executable file triggers installation but must survive a failed build.
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(binary, []byte("previous file"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := Install(t.Context(), root, "fixture", &output); err == nil {
		t.Fatal("accepted invalid source")
	}
	data, err := os.ReadFile(binary)
	if runtime.GOOS == "windows" {
		if !os.IsNotExist(err) {
			t.Fatal("published a failed build", err)
		}
	} else if err != nil || string(data) != "previous file" {
		t.Fatal(string(data), err)
	}
	entries, _ := os.ReadDir(filepath.Dir(binary))
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "-build-") {
			t.Fatal("left temporary build output", entry.Name())
		}
	}
}
