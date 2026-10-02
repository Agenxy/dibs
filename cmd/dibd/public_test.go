package main

import (
	"flag"
	"io"
	"testing"
)

func cloudFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("dibd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func TestPublicProxyFlagsEnterThroughProductionParser(t *testing.T) {
	fs := flag.NewFlagSet("dibd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerDaemonFlags(fs)
	if err := fs.Parse([]string{"--public-url", "https://board.example.com", "--public-addr", "127.0.0.1:4778"}); err != nil {
		t.Fatalf("cloud ingress cannot be configured through dibd's real flags: %v", err)
	}
}

func TestPublicConfigurationRefusesInsecureOrAmbiguousIngress(t *testing.T) {
	for _, args := range [][]string{
		{"--public-host", "board.example.com"},
		{"--public-host", "*.example.com", "--acme-accept-terms"},
		{"--public-host", "127.0.0.1", "--acme-accept-terms"},
		{"--public-host", "board.example.com", "--acme-accept-terms", "--public-addr", ":8443"},
		{"--public-host", "board.example.com", "--public-url", "https://board.example.com"},
		{"--public-url", "http://board.example.com"},
		{"--public-url", "https://board.example.com/mcp"},
		{"--public-url", "https://user:pass@board.example.com"},
		{"--public-url", "https://board.example.com?q=x"},
		{"--public-url", "https://board.example.com#fragment"},
		{"--public-url", "https://*.example.com"},
		{"--public-url", "https://board.example.com", "--public-addr", "0.0.0.0:4778"},
		{"--public-addr", "127.0.0.1:4778"},
	} {
		fs := cloudFlagSet()
		opts, _ := registerDaemonFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if _, err := resolvePublic(opts); err == nil {
			t.Fatalf("insecure/ambiguous configuration accepted: %v", args)
		}
	}
	fs := cloudFlagSet()
	opts, _ := registerDaemonFlags(fs)
	if err := fs.Parse([]string{"--public-host", "Board.Example.com", "--acme-accept-terms"}); err != nil {
		t.Fatal(err)
	}
	if c, err := resolvePublic(opts); err != nil || c.Host != "board.example.com" || c.Addr != ":443" {
		t.Fatalf("ACME configuration: %v %v", c, err)
	}
}
