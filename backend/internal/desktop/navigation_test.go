package desktop

import (
	"net"
	"testing"
)

type staticAddr string

func (a staticAddr) Network() string { return "tcp" }
func (a staticAddr) String() string  { return string(a) }

func TestNavigationURL(t *testing.T) {
	cases := []struct {
		address string
		want    string
	}{
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"0.0.0.0:9090", "http://127.0.0.1:9090"},
		{"[::]:8081", "http://[::1]:8081"},
		{"[::1]:8082", "http://[::1]:8082"},
	}
	for _, tc := range cases {
		t.Run(tc.address, func(t *testing.T) {
			got, err := NavigationURL(staticAddr(tc.address))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("NavigationURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNavigationURLRejectsInvalidAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "127.0.0.1:http", "[::1]"} {
		t.Run(address, func(t *testing.T) {
			if _, err := NavigationURL(staticAddr(address)); err == nil {
				t.Fatalf("NavigationURL(%q) succeeded", address)
			}
		})
	}
	if _, err := NavigationURL(net.Addr(nil)); err == nil {
		t.Fatal("NavigationURL(nil) succeeded")
	}
}
