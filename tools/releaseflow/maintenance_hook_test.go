package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type fixtureGCReport struct {
	Parent int `json:"parent"`
}

// Git calls this native executable as gc.recentObjectsHook. It gates the REAL
// Git prune/pack-objects process; it neither replaces Git nor writes Git objects.
// No stdout: the hook's output is an object-id protocol, not test output.
func releaseFixtureGCHook() int {
	dir := os.Getenv("DIBS_TEST_RELEASE_GC_CONTROL")
	if dir == "" {
		return 30
	}
	data, err := json.Marshal(fixtureGCReport{Parent: os.Getppid()})
	if err != nil {
		return 31
	}
	path := filepath.Join(dir, "hook-"+strconv.Itoa(os.Getpid())+".json")
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return 32
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return 33
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(time.Minute)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			return 0
		} else if !errors.Is(err, os.ErrNotExist) {
			return 34
		}
		select {
		case <-deadline.C:
			return 35
		case <-tick.C:
		}
	}
}
