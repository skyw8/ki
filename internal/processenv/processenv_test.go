package processenv

import "testing"

func TestProxyEnvironmentFromFiltersKeys(t *testing.T) {
	got := ProxyEnvironmentFrom([]string{
		"PATH=/bin",
		"HTTP_PROXY=http://proxy.example:8080",
		"https_proxy=http://proxy.example:8080",
		"NO_PROXY=localhost",
		"KI_HOME=/tmp/ki",
	})
	want := []string{
		"HTTP_PROXY=http://proxy.example:8080",
		"https_proxy=http://proxy.example:8080",
		"NO_PROXY=localhost",
	}
	if len(got) != len(want) {
		t.Fatalf("proxy environment = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("proxy environment = %#v, want %#v", got, want)
		}
	}
}

func TestWithProxyEnvironmentFromPreservesChildValues(t *testing.T) {
	got := WithProxyEnvironmentFrom(
		[]string{"PATH=/bin", "http_proxy=http://child.example:8080"},
		[]string{
			"HTTP_PROXY=http://parent.example:8080",
			"HTTPS_PROXY=http://parent.example:8443",
			"KI_HOME=/tmp/ki",
		},
	)
	want := []string{
		"PATH=/bin",
		"http_proxy=http://child.example:8080",
		"HTTPS_PROXY=http://parent.example:8443",
	}
	if len(got) != len(want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("environment = %#v, want %#v", got, want)
		}
	}
}
