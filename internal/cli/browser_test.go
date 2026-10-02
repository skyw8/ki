package cli

import (
	"net/url"
	"testing"
)

func TestBrowserURL(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want string
	}{
		{"127.0.0.1:19800", "http://127.0.0.1:19800/"},
		{"localhost:19800", "http://localhost:19800/"},
		{"192.168.1.5:19800", "http://192.168.1.5:19800/"},
		{":19800", "http://127.0.0.1:19800/"},
		{"0.0.0.0:19800", "http://127.0.0.1:19800/"},
		{"[::]:19800", "http://127.0.0.1:19800/"},
		{"[::1]:19800", "http://[::1]:19800/"},
		{"localhost", "http://localhost/"},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			if got := browserURL(tc.addr, ""); got != tc.want {
				t.Fatalf("URL without token = %q, want %q", got, tc.want)
			}
			// Include delimiters so the token cannot inject fragment fields.
			token := "secret+/=&?# %你好"
			got := browserURL(tc.addr, token)
			u, err := url.Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			if u.RawQuery != "" || u.User != nil {
				t.Fatalf("secret must only appear in fragment: %q", got)
			}
			fragment, err := url.ParseQuery(u.EscapedFragment())
			if err != nil || len(fragment) != 1 || fragment.Get("token") != token {
				t.Fatalf("fragment = %q, error = %v", u.Fragment, err)
			}
			u.Fragment = ""
			u.RawFragment = ""
			if u.String() != tc.want {
				t.Fatalf("base URL = %q, want %q", u.String(), tc.want)
			}
		})
	}
}
