package app

import "testing"

func TestBrowserURL(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{"127.0.0.1:6800", "http://127.0.0.1:6800/#/search"},
		{":6800", "http://127.0.0.1:6800/#/search"},
		{"0.0.0.0:6800", "http://127.0.0.1:6800/#/search"},
		{"[::]:6800", "http://[::1]:6800/#/search"},
		{"[::1]:6800", "http://[::1]:6800/#/search"},
		{"bad-address", ""},
	}
	for _, tc := range cases {
		if got := browserURL(tc.addr); got != tc.want {
			t.Errorf("browserURL(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}
