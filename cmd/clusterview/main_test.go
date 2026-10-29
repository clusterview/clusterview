package main

import "testing"

func TestCleanBasePath(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "/": "", "clusterview": "/clusterview", "/clusterview/": "/clusterview",
		" /tools/clusterview ": "/tools/clusterview",
	} {
		if got, err := cleanBasePath(in); err != nil || got != want {
			t.Errorf("cleanBasePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"/a?b", "/a#b", "/a//b", "/../x", "/a b", "/a%2f"} {
		if _, err := cleanBasePath(bad); err == nil {
			t.Errorf("cleanBasePath(%q) should be refused", bad)
		}
	}
}
