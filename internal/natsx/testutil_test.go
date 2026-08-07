package natsx

import (
	"net"
	"testing"
)

func reserveAddrs(t *testing.T, n int) []int {
	t.Helper()
	var out []int
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ln.Addr().(*net.TCPAddr).Port)
		ln.Close()
	}
	return out
}
