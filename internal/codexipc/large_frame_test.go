// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import (
	"encoding/binary"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

type historyBytes struct{}

func (historyBytes) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = 'x'
	}
	return len(b), nil
}

func TestNativeUnsolicited50MiBFrameUsesFlatMemory(t *testing.T) {
	for _, megabytes := range []int{10, 50} {
		t.Run(strconv.Itoa(megabytes)+"MiB", func(t *testing.T) {
			reader, writer := net.Pipe()
			t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
			done := make(chan error, 1)
			ready := make(chan struct{})
			go func() {
				<-ready
				prefix := `{"type":"broadcast","method":"thread-stream-state-changed","params":{"history":"`
				suffix := `"}}`
				size := megabytes << 20
				var header [4]byte
				// #nosec G115 -- fixture frames are exactly 10 or 50 MiB plus fixed JSON.
				binary.LittleEndian.PutUint32(header[:], uint32(size+len(prefix)+len(suffix)))
				stream := io.MultiReader(strings.NewReader(string(header[:])), strings.NewReader(prefix),
					io.LimitReader(historyBytes{}, int64(size)), strings.NewReader(suffix))
				if _, err := io.Copy(writer, stream); err != nil {
					done <- err
					return
				}
				body := `{"type":"response","requestId":"matched","method":"initialize","resultType":"success","result":{"clientId":"fixture"}}`
				// #nosec G115 -- fixed short receipt literal.
				binary.LittleEndian.PutUint32(header[:], uint32(len(body)))
				_, err := io.Copy(writer, strings.NewReader(string(header[:])+body))
				done <- err
			}()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			close(ready)
			c := client{conn: reader}
			f, err := c.read()
			if err != nil || f.Type != "" {
				t.Fatalf("large unsolicited frame stopped reader: %+v %v", f, err)
			}
			f, err = c.read()
			if err != nil || f.RequestID != "matched" {
				t.Fatalf("draining history lost next receipt boundary: %+v %v", f, err)
			}
			if err = <-done; err != nil {
				t.Fatal("fixture:", err)
			}
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			if allocated > 2<<20 {
				t.Fatalf("%d MiB frame allocated %d bytes, want less than 2 MiB", megabytes, allocated)
			}
			t.Logf("%d MiB frame: %d allocated bytes (includes streaming fixture)", megabytes, allocated)
		})
	}
}
