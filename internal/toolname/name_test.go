package toolname

import "testing"

func TestCanonicalAndAliases(t *testing.T) {
	for _, test := range []struct{ native, canonical, pascal string }{{"Read", "read", "Read"}, {"write_stdin", "write_stdin", "WriteStdin"}, {"HTTPFetch", "http_fetch", "HttpFetch"}, {"GetURL2", "get_url2", "GetUrl2"}, {"apply_patch", "apply_patch", "ApplyPatch"}, {"FollowupTask", "followup_task", "FollowupTask"}} {
		got, err := Canonical(test.native)
		if err != nil || got != test.canonical {
			t.Fatalf("%s: %s %v", test.native, got, err)
		}
		if Pascal(got) != test.pascal {
			t.Fatal(test.native)
		}
		if !Equal(test.canonical, test.native) || !Equal(test.pascal, test.native) {
			t.Fatal(test.native)
		}
	}
	if Equal("READ", "read") {
		t.Fatal("arbitrary case matching accepted")
	}
	for _, bad := range []string{"", "_read", "1read", "read.file", "read-file"} {
		if _, err := Canonical(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
