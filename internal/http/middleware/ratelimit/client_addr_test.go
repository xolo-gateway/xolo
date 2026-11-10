package ratelimit

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientAddr(t *testing.T) {
	ingress := []netip.Prefix{netip.MustParsePrefix("10.244.0.0/16"), netip.MustParsePrefix("192.0.2.10/32")}

	cases := []struct {
		name    string
		trust   bool
		proxies []netip.Prefix
		remote  string
		xff     []string
		xri     string
		want    string
	}{
		{name: "headers ignored", trust: false, remote: "203.0.113.5:1234", xff: []string{"198.51.100.1"}, want: "203.0.113.5"},
		{name: "legacy left-most", trust: true, remote: "10.244.1.2:1234", xff: []string{"198.51.100.1, 203.0.113.9"}, want: "198.51.100.1"},
		{name: "legacy real ip", trust: true, remote: "10.244.1.2:1234", xri: "198.51.100.7", want: "198.51.100.7"},
		// The client forged the left-most entry; the ingress appended the
		// address it really came from.
		{name: "forged entry skipped", trust: true, proxies: ingress, remote: "10.244.1.2:1234", xff: []string{"1.2.3.4, 203.0.113.9"}, want: "203.0.113.9"},
		// Load balancer then ingress: both trusted, the client is before them.
		{name: "two trusted hops", trust: true, proxies: ingress, remote: "10.244.1.2:1234", xff: []string{"203.0.113.9, 192.0.2.10"}, want: "203.0.113.9"},
		{name: "several header lines", trust: true, proxies: ingress, remote: "10.244.1.2:1234", xff: []string{"1.2.3.4", "203.0.113.9"}, want: "203.0.113.9"},
		// Not from a trusted proxy: its headers say nothing.
		{name: "untrusted peer", trust: true, proxies: ingress, remote: "203.0.113.5:1234", xff: []string{"198.51.100.1"}, want: "203.0.113.5"},
		{name: "only trusted hops", trust: true, proxies: ingress, remote: "10.244.1.2:1234", xff: []string{"10.244.3.3"}, want: "10.244.3.3"},
		// A proxy that does not rewrite X-Real-Ip passes the client's own on.
		{name: "real ip ignored with trusted proxies", trust: true, proxies: ingress, remote: "10.244.1.2:1234", xri: "198.51.100.7", want: "10.244.1.2"},
		{name: "no header from trusted peer", trust: true, proxies: ingress, remote: "10.244.1.2:1234", want: "10.244.1.2"},
		{name: "hop with a port", trust: true, proxies: ingress, remote: "10.244.1.2:1234", xff: []string{"203.0.113.9:4711, 10.244.3.3:80"}, want: "203.0.113.9"},
		{name: "ipv4-mapped peer", trust: true, proxies: ingress, remote: "[::ffff:10.244.1.2]:1234", xff: []string{"203.0.113.9"}, want: "203.0.113.9"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			for _, line := range tc.xff {
				r.Header.Add("X-Forwarded-For", line)
			}
			if tc.xri != "" {
				r.Header.Set("X-Real-Ip", tc.xri)
			}
			if got := clientAddr(r, tc.trust, tc.proxies); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
