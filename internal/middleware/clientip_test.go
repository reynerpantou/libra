package middleware

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("10.0.0.5/32")}
	cases := []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		xff     []string
		want    string
	}{
		{"no proxies configured ignores header", nil, "203.0.113.9:5000", []string{"1.1.1.1"}, "203.0.113.9"},
		{"untrusted peer ignores header", proxies, "203.0.113.9:5000", []string{"1.1.1.1"}, "203.0.113.9"},
		{"trusted proxy gives visitor", proxies, "172.18.0.1:5000", []string{"198.51.100.7"}, "198.51.100.7"},
		{"spoofed left entries are skipped", proxies, "172.18.0.1:5000", []string{"1.1.1.1, 198.51.100.7"}, "198.51.100.7"},
		{"chained trusted hops", proxies, "172.18.0.1:5000", []string{"198.51.100.7, 10.0.0.5"}, "198.51.100.7"},
		{"split header lines", proxies, "172.18.0.1:5000", []string{"1.1.1.1", "198.51.100.7"}, "198.51.100.7"},
		{"garbage falls back to peer", proxies, "172.18.0.1:5000", []string{"not-an-ip"}, "172.18.0.1"},
		{"no header falls back to peer", proxies, "172.18.0.1:5000", nil, "172.18.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/login", nil)
		r.RemoteAddr = c.remote
		for _, v := range c.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := ClientIP(c.trusted)(r); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
