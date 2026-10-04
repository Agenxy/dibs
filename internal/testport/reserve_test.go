package testport

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestBindRetriesTheActualCollisionWithFreshOwnership(t *testing.T) {
	var previous, address string
	var listener net.Listener
	attempts := 0
	bound := Bind(t, "tcp", "127.0.0.1:0", func(addr string) {
		address = addr
		competitor, err := net.Listen("tcp", addr)
		if err == nil {
			_ = competitor.Close()
			t.Fatal("preparation received an unowned address")
		}
		if attempts > 0 && addr == previous {
			t.Fatal("collision retry reused the old address")
		}
	}, func() error {
		attempts++
		if attempts == 1 {
			previous = address
			competitor, err := net.Listen("tcp", address)
			if err != nil {
				t.Fatalf("setup: competitor could not bind: %v", err)
			}
			defer func() { _ = competitor.Close() }()
			listener, err = net.Listen("tcp", address)
			if !errors.Is(err, syscall.EADDRINUSE) {
				t.Fatalf("setup: actual collision did not return EADDRINUSE: %v", err)
			}
			return err
		}
		var err error
		listener, err = net.Listen("tcp", address)
		return err
	})
	defer func() { _ = listener.Close() }()
	if attempts != 2 || bound != listener.Addr().String() {
		t.Fatalf("bind did not recover the actual collision: attempts=%d bound=%s", attempts, bound)
	}
}

func TestOutageSelectionChangesAddressOnlyBeforeTheTestStarts(t *testing.T) {
	port := Reserve(t, "tcp", "127.0.0.1:0")
	port.Release(t)
	competitor, err := net.Listen("tcp", port.Addr)
	if err != nil {
		t.Fatalf("setup: competitor could not bind: %v", err)
	}
	defer func() { _ = competitor.Close() }()
	address := port.ReleaseForOutage(t)
	if address == port.Addr {
		t.Fatal("outage selector returned the occupied address")
	}
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("selected outage address is listening")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("outage premise changed: %v", err)
	}
}

func TestBindErrorHelper(t *testing.T) {
	mode := os.Getenv("DIBS_TEST_BIND_ERROR")
	if mode == "" {
		return
	}
	attempt := 0
	Bind(t, "tcp", "127.0.0.1:0", func(string) {}, func() error {
		attempt++
		if _, err := fmt.Fprintf(os.Stdout, "bind-attempt=%d\n", attempt); err != nil {
			t.Fatal(err)
		}
		if mode == "collision" {
			return fmt.Errorf("wrapped bind: %w", syscall.EADDRINUSE)
		}
		return errors.New("address already in use is prose, not a syscall error")
	})
}

func TestBindRejectsOtherErrorsAndBoundsCollisions(t *testing.T) {
	for _, mode := range []string{"other", "collision"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBindErrorHelper$")
			cmd.Env = append(os.Environ(), "DIBS_TEST_BIND_ERROR="+mode)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("failing fixture returned success")
			}
			want := 1
			if mode == "collision" {
				want = 3
			}
			if got := strings.Count(string(output), "bind-attempt="); got != want {
				t.Fatalf("wrong retry count: got %d, want %d\n%s", got, want, output)
			}
		})
	}
}
