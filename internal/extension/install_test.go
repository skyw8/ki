package extension

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRuntimeInstallWhenValidation(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		name, when, command string
		want                error
	}{
		{"default PATH command", "", "node", nil},
		{"always PATH command", "always", "node", nil},
		{"missing nested binary", "missing", "bin/sidecar", nil},
		{"missing explicit local binary", "missing", "./sidecar", nil},
		{"unknown mode", "never", "bin/sidecar", errUnknownInstallWhen},
		{"missing PATH command", "missing", "node", errInstallMissingNeedsLocalCommand},
		{"missing absolute command", "missing", filepath.Join(root, "sidecar"), errPathMustBeRelative},
		{"missing POSIX root", "missing", "/bin/sidecar", errPathMustBeRelative},
		{"missing Windows root", "missing", "C:/bin/sidecar", errPathMustBeRelative},
		{"missing escaping command", "missing", "../bin/sidecar", errPathEscapesPackage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := Manifest{Name: "install-fixture", Capabilities: []string{"lifecycle"}, Runtime: RuntimeSpec{Kind: runtimeRPC, Command: tt.command, InstallWhen: tt.when}}
			err := validateManifest(root, m)
			if !errors.Is(err, tt.want) {
				t.Fatalf("validation error=%v want=%v", err, tt.want)
			}
		})
	}
	m := Manifest{Name: "install-fixture", Runtime: RuntimeSpec{Kind: runtimeNone, Command: "bin/sidecar", InstallWhen: installMissing}}
	if err := validateManifest(root, m); !errors.Is(err, errInstallMissingNeedsLocalCommand) {
		t.Fatalf("missing installation accepted without RPC runtime: %v", err)
	}
}

func TestRuntimeInstallMissingRejectsOutsideSymlinkAncestors(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "bin")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	spec := RuntimeSpec{Kind: runtimeRPC, Command: "bin/not-created-yet/sidecar", InstallWhen: installMissing}
	if err := validateRuntimeInstall(root, spec); !errors.Is(err, errPathEscapesPackage) {
		t.Fatalf("missing command with external parent accepted: %v", err)
	}
}

// TestRuntimeInstallFixture runs the current test executable as a portable
// install command; no shell, compiler, or source is needed by its child process.
func TestRuntimeInstallFixture(t *testing.T) {
	if os.Getenv("KI_INSTALL_FIXTURE") != "1" {
		return
	}
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(17)
	}
	marker, err := os.OpenFile(os.Getenv("KI_INSTALL_MARKER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail(err)
	}
	if _, err := marker.WriteString("install\n"); err != nil {
		fail(err)
	}
	if err := marker.Close(); err != nil {
		fail(err)
	}
	switch os.Getenv("KI_INSTALL_ACTION") {
	case "tree":
		binary, err := os.Executable()
		if err != nil {
			fail(err)
		}
		child := exec.Command(binary, "-test.run=^TestRuntimeInstallTreeChild$")
		child.Env = os.Environ()
		child.Stdout, child.Stderr = os.Stderr, os.Stderr
		if err := child.Run(); err != nil {
			fail(err)
		}
	case "copy":
		data, err := os.ReadFile(os.Getenv("KI_INSTALL_SOURCE"))
		if err != nil {
			fail(err)
		}
		target := os.Getenv("KI_INSTALL_TARGET")
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			fail(err)
		}
		if err := os.WriteFile(target, data, 0o700); err != nil {
			fail(err)
		}
	case "fail":
		fail(errors.New("fixture install failed"))
	case "noop":
	case "env":
		var expected map[string]string
		if err := json.Unmarshal([]byte(os.Getenv("KI_INSTALL_EXPECTED_ENV")), &expected); err != nil {
			fail(errors.New("invalid fixture environment expectation"))
		}
		for key, value := range expected {
			if os.Getenv(key) != value {
				fail(fmt.Errorf("installer environment mismatch for %s", key))
			}
		}
	default:
		fail(errors.New("unknown fixture install action"))
	}
	os.Exit(0)
}

func TestRuntimeInstallTreeChild(t *testing.T) {
	addr := os.Getenv("KI_INSTALL_READY_ADDR")
	if addr == "" {
		return
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		os.Exit(17)
	}
	if _, err := io.WriteString(conn, "ready\n"); err != nil {
		os.Exit(17)
	}
	// The test owns the other end, so even a broken cancellation path cannot
	// leave the helper behind after cleanup closes the connection.
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.Close()
	os.Exit(0)
}

func TestRuntimeInstallCancellationKillsDescendants(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	d, _, _ := installDescriptor(t, "", "tree", installAlways)
	d.manifest.Runtime.Env["KI_INSTALL_READY_ADDR"] = listener.Addr().String()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	home := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- installRuntime(ctx, d.root, d.manifest.Runtime, sidecarEnv(d, "", home, ""))
	}()
	// Accept/read deadlines are failure bounds, not fixed sleeps. Cancellation
	// starts only after the compiler descendant has explicitly signalled ready.
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	if ready, err := reader.ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatalf("descendant readiness = %q, %v", ready, err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled install succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled install did not return")
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("unexpected descendant output")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatal("compiler descendant survived installer cancellation")
	}
}

func TestRuntimeInstallEnvironmentInheritsBuildConfigurationAndOverrides(t *testing.T) {
	d, _, _ := installDescriptor(t, "", "env", installAlways)
	expected := map[string]string{}
	for _, key := range []string{"KI_BUILD_TOOLCHAIN_SENTINEL", "GOCACHE", "RUSTUP_HOME", "CARGO_HOME", "LIBCLANG_PATH", "INCLUDE", "LIB", "USERPROFILE", "LOCALAPPDATA", "TEMP"} {
		value := t.TempDir()
		t.Setenv(key, value)
		expected[key] = value
	}
	t.Setenv("HTTP_PROXY", "http://inherited.invalid")
	t.Setenv("HTTPS_PROXY", "http://inherited-secure.invalid")
	t.Setenv("KI_HOME", "parent-home")
	t.Setenv("KI_SESSION_ID", "parent-session")
	t.Setenv("KI_CWD", "parent-cwd")
	if runtime.GOOS != "windows" {
		expected["cargo_home"] = t.TempDir()
		t.Setenv("cargo_home", expected["cargo_home"])
	}
	home := t.TempDir()
	expected["CARGO_HOME"] = t.TempDir()
	expected["HTTP_PROXY"] = "http://override.invalid"
	expected["HTTPS_PROXY"] = "http://inherited-secure.invalid"
	expected["KI_EXTENSION"] = d.Name
	expected["KI_EXTENSION_ROOT"] = d.root
	expected["KI_HOME"] = home
	expected["KI_SESSION_ID"], expected["KI_CWD"] = "", ""
	d.manifest.Runtime.Env["CARGO_HOME"] = expected["CARGO_HOME"]
	d.manifest.Runtime.Env["HTTP_PROXY"] = expected["HTTP_PROXY"]
	raw, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	d.manifest.Runtime.Env["KI_INSTALL_EXPECTED_ENV"] = string(raw)
	scoped := sidecarEnv(d, "", home, "")
	for _, entry := range scoped {
		key, _, _ := strings.Cut(entry, "=")
		if key == "KI_BUILD_TOOLCHAIN_SENTINEL" || key == "GOCACHE" || key == "RUSTUP_HOME" || key == "LIBCLANG_PATH" || key == "INCLUDE" || key == "LIB" {
			t.Fatalf("runtime sidecar inherited build-only variable %s", key)
		}
	}
	if err := installRuntime(t.Context(), d.root, d.manifest.Runtime, scoped); err != nil {
		t.Fatal("install hook did not receive configured build environment", err)
	}
}

func TestEnvironmentKeyNormalizationMatchesUnixAndWindows(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, key := range []string{"HTTP_PROXY", "http_proxy", "Cargo_HOME", "cargo_home"} {
			want := key
			if goos == "windows" {
				want = strings.ToUpper(key)
			}
			if got := normalizeEnvironmentKey(key, goos); got != want {
				t.Fatalf("%s key normalization failed for %s", goos, key)
			}
		}
	}
}

func TestRuntimeEnvironmentOverridePreservesHostCaseSemantics(t *testing.T) {
	env := setEnvironmentValue([]string{"HTTP_PROXY=upper"}, "http_proxy", "lower")
	env = setEnvironmentValue(env, "HTTP_PROXY", "override")
	want := []string{"HTTP_PROXY=override", "http_proxy=lower"}
	if runtime.GOOS == "windows" {
		want = []string{"HTTP_PROXY=override"}
	}
	if len(env) != len(want) {
		t.Fatalf("environment key count=%d want=%d", len(env), len(want))
	}
	for index := range want {
		if env[index] != want[index] {
			t.Fatalf("incorrect override at environment index %d", index)
		}
	}
}

func TestInstallEnvironmentPreservesHostCaseSemantics(t *testing.T) {
	t.Setenv("KI_ENV_CASE_FIXTURE", "parent-upper")
	if runtime.GOOS != "windows" {
		t.Setenv("ki_env_case_fixture", "parent-lower")
	}
	env := installEnvironment([]string{"KI_ENV_CASE_FIXTURE=override"})
	seen := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "KI_ENV_CASE_FIXTURE") {
			seen[key] = value
		}
	}
	if seen["KI_ENV_CASE_FIXTURE"] != "override" {
		t.Fatal("install environment lost explicit override")
	}
	if runtime.GOOS == "windows" {
		if len(seen) != 1 {
			t.Fatal("Windows install environment retained a case alias")
		}
	} else if seen["ki_env_case_fixture"] != "parent-lower" || len(seen) != 2 {
		t.Fatal("Unix install environment lost a distinct lowercase key")
	}
}

func installDescriptor(t *testing.T, source, action, when string) (Descriptor, string, string) {
	t.Helper()
	root := t.TempDir()
	target := filepath.Join(root, "bin", "sidecar"+fixtureExeSuffix())
	marker := filepath.Join(root, "install-calls.txt")
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Name: "install-fixture", Capabilities: []string{"lifecycle"}, Runtime: RuntimeSpec{
		Kind: runtimeRPC, Command: "bin/sidecar", InstallWhen: when,
		Install: []string{testBinary, "-test.run=^TestRuntimeInstallFixture$"},
		Env: map[string]string{
			"PATH": t.TempDir(), "KI_INSTALL_FIXTURE": "1", "KI_INSTALL_ACTION": action,
			"KI_INSTALL_SOURCE": source, "KI_INSTALL_TARGET": target, "KI_INSTALL_MARKER": marker,
		},
	}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extension.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	d, ok := loadPackage(root)
	if !ok || d.Error != "" {
		t.Fatalf("load install fixture: ok=%v error=%s", ok, d.Error)
	}
	d.Enabled = true
	return d, marker, target
}

func copyInstalledSidecar(t *testing.T, source, target string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o700); err != nil {
		t.Fatal(err)
	}
}

func startInstalledSidecar(t *testing.T, d Descriptor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := startRPC(ctx, d, "sess", t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	client.close()
}

func TestRuntimeInstallMissingExistingBinarySkipsToolchain(t *testing.T) {
	source := buildTestSidecar(t)
	d, marker, target := installDescriptor(t, source, "fail", installMissing)
	copyInstalledSidecar(t, source, target)
	startInstalledSidecar(t, d)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("existing binary invoked install hook: %v", err)
	}
}

func TestRuntimeInstallMissingBuildsOnceBeforeInitialize(t *testing.T) {
	d, marker, target := installDescriptor(t, buildTestSidecar(t), "copy", installMissing)
	startInstalledSidecar(t, d)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("install did not create runtime binary: %v", err)
	}
	startInstalledSidecar(t, d)
	if data, err := os.ReadFile(marker); err != nil || string(data) != "install\n" {
		t.Fatalf("install count after two starts: %v %q", err, data)
	}
}

func TestRuntimeInstallAlwaysPreservesEveryStartHook(t *testing.T) {
	source := buildTestSidecar(t)
	for _, when := range []string{"", installAlways} {
		t.Run("mode="+when, func(t *testing.T) {
			d, marker, target := installDescriptor(t, source, "noop", when)
			copyInstalledSidecar(t, source, target)
			startInstalledSidecar(t, d)
			startInstalledSidecar(t, d)
			if data, err := os.ReadFile(marker); err != nil || string(data) != "install\ninstall\n" {
				t.Fatalf("always install count: %v %q", err, data)
			}
		})
	}
}

func TestRuntimeInstallMissingFailureStopsSidecar(t *testing.T) {
	d, marker, target := installDescriptor(t, buildTestSidecar(t), "fail", installMissing)
	if _, err := startRPC(t.Context(), d, "sess", t.TempDir(), t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "runtime.install") {
		t.Fatalf("failed install must stop launch: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("failed install created runtime: %v", err)
	}
}

func TestRuntimeInstallMissingDirectoryDoesNotInvokeInstaller(t *testing.T) {
	d, marker, _ := installDescriptor(t, buildTestSidecar(t), "fail", installMissing)
	if err := os.MkdirAll(filepath.Join(d.root, "bin", "sidecar"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := startRPC(t.Context(), d, "sess", t.TempDir(), t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("invalid runtime file must fail before install: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("invalid runtime file invoked installer: %v", err)
	}
}
