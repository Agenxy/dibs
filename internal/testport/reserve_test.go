package testport

import (
	"net"
	"testing"
)

func TestReservationOwnsPortUntilExplicitRelease(t *testing.T) {
	for _, c := range []struct{ network, address string }{
		{"tcp4", "127.0.0.1:0"}, {"tcp6", "[::1]:0"},
	} {
		t.Run(c.network, func(t *testing.T) {
			port := Reserve(t, c.network, c.address)
			competitor, err := net.Listen(c.network, port.Addr)
			if err == nil {
				_ = competitor.Close()
				t.Fatal("another fixture stole the reserved port")
			}
			port.Release(t)
			listener, err := net.Listen(c.network, port.Addr)
			if err != nil {
				t.Fatalf("released port cannot serve: %v", err)
			}
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
