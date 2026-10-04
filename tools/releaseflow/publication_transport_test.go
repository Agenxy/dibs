package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPublicationAPIReadsUseOnlyTheJobToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "job-token-fixture")
	for _, endpoint := range []string{
		"https://api.github.com/repos/example/scratch/actions/runs/123",
		"https://github.com/example/scratch/releases/download/ref/file",
		"https://release-assets.githubusercontent.com/file",
		"http://api.github.com/repos/example/scratch",
		"https://api.github.com:8443/repos/example/scratch",
		"https://api.github.com.example.invalid/file",
	} {
		t.Run(endpoint, func(t *testing.T) {
			c := config{publicClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				want := ""
				if r.URL.Scheme == "https" && r.URL.Host == "api.github.com" {
					want = "Bearer job-token-fixture"
				}
				if r.Header.Get("Authorization") != want {
					t.Error("job token absent at API origin, or exposed outside it")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("bounded")), Header: make(http.Header)}, nil
			})}}
			if _, err := publicBytes(context.Background(), c, endpoint, 16); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublicationAPIMissingTokenRefusesBeforeHTTP(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	c := config{publicClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("missing job token silently used anonymous API fallback")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}}
	if _, err := publicBytes(context.Background(), c, "https://api.github.com/repos/example/scratch", 16); err == nil {
		t.Fatal("missing job token accepted")
	}
}

func TestPublicationRedirectNeverCarriesJobTokenToAssetOrigins(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "job-token-fixture")
	for _, target := range []string{
		"https://release-assets.githubusercontent.com/file",
		"https://github.com/example/file",
		"https://api.github.com:8443/file",
	} {
		t.Run(target, func(t *testing.T) { checkPublicationRedirect(t, target) })
	}
}

func checkPublicationRedirect(t *testing.T, target string) {
	t.Helper()
	calls := 0
	c := config{publicClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if r.Header.Get("Authorization") != "Bearer job-token-fixture" {
				t.Error("initial API request did not use job token")
			}
			return &http.Response{StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": {target}}}, nil
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("job token leaked through a cross-origin redirect")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("asset")), Header: make(http.Header)}, nil
	})}}
	if _, err := publicBytes(context.Background(), c, "https://api.github.com/repos/example/scratch", 16); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("redirect setup did not run")
	}
}

func TestPublicationRefusesUnsafeRedirectBeforeSending(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "job-token-fixture")
	for _, target := range []string{"http://api.github.com/file", "https://example.invalid/file", "https://user@api.github.com/file"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			c := config{publicClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if calls != 1 {
					t.Fatal("unsafe redirect reached transport")
				}
				return &http.Response{StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": {target}}}, nil
			})}}
			if _, err := publicBytes(context.Background(), c, "https://api.github.com/repos/example/scratch", 16); err == nil || calls != 1 {
				t.Fatal("unsafe redirect was not refused at the real client boundary")
			}
		})
	}
}
