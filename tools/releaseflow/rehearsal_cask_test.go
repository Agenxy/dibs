package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/selfupdate"
)

func generatedCaskFixture(t *testing.T, c config, repo, tag string) string {
	t.Helper()
	var body strings.Builder
	fmt.Fprintf(&body, "cask \"dibs\" do\n version \"%s\"\n", c.version)
	for _, name := range assets(c.version) {
		if !strings.HasSuffix(name, ".tar.gz") {
			continue
		}
		sum, err := digest(filepath.Join("dist", name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&body, " sha256 \"%s\"\n url \"https://github.com/%s/releases/download/%s/%s\"\n",
			sum, repo, tag, strings.ReplaceAll(name, c.version, "#{version}"))
	}
	body.WriteString("end\n")
	return body.String()
}

// Enter where the publisher does: stageUnsigned must bind scratch URLs BEFORE
// signing, and must leave production's generated cask byte-for-byte unchanged.
func TestStageUnsignedBindsOnlyScratchCaskToVerifiedPayload(t *testing.T) {
	for _, target := range []string{"production", "rehearsal"} {
		t.Run(target, func(t *testing.T) {
			t.Chdir(t.TempDir())
			c := config{version: "0.0.11", sha: strings.Repeat("a", 40)}
			c.destination = productionTarget(c)
			if target == "rehearsal" {
				var err error
				c.destination, err = rehearsalTarget(c)
				if err != nil {
					t.Fatal(err)
				}
			}
			fixtureAssets(t, c, "dist")
			write(t, "dist/checksums.txt", "")
			d := targetOf(c)
			generated := generatedCaskFixture(t, c, d.repository, "v#{version}")
			write(t, "dist/homebrew/Casks/dibs.rb", generated)
			if err := stageUnsigned(c); err != nil {
				t.Fatal(err)
			}
			staged, err := os.ReadFile("dist/dibs.rb")
			if err != nil {
				t.Fatal(err)
			}
			if target == "production" && !bytes.Equal(staged, []byte(generated)) {
				t.Fatal("production cask was rewritten")
			}
			if target == "rehearsal" {
				prefix := "https://github.com/" + d.repository + "/releases/download/" + d.tag + "/"
				if strings.Count(string(staged), prefix) != 3 || strings.Contains(string(staged), "/releases/download/v") {
					t.Fatalf("scratch cask is not bound to three exact public payload URLs: %s", staged)
				}
			}
			original, err := os.ReadFile("dist/homebrew/Casks/dibs.rb")
			if err != nil || !bytes.Equal(original, []byte(generated)) {
				t.Fatal("GoReleaser input was mutated")
			}
			if err := validateDryCask(c, "dist"); err != nil {
				t.Fatal(err)
			}
			checksums, err := os.ReadFile("dist/checksums.txt")
			if err != nil {
				t.Fatal(err)
			}
			want, err := selfupdate.ChecksumFor(string(checksums), "dibs.rb")
			if err != nil {
				t.Fatal(err)
			}
			got, err := digest("dist/dibs.rb")
			if err != nil || got != want {
				t.Fatal("signing input does not cover the final cask bytes")
			}
		})
	}
}

func TestScratchCaskRejectsForeignOrUnverifiedGeneratedPairsBeforeStaging(t *testing.T) {
	for _, mutation := range []string{"production-repository", "foreign-tag", "wrong-version", "bad-digest", "missing-pair", "duplicate-pair"} {
		t.Run(mutation, func(t *testing.T) {
			t.Chdir(t.TempDir())
			c := config{version: "0.0.11", sha: strings.Repeat("b", 40)}
			var err error
			c.destination, err = rehearsalTarget(c)
			if err != nil {
				t.Fatal(err)
			}
			fixtureAssets(t, c, "dist")
			write(t, "dist/checksums.txt", "")
			d := targetOf(c)
			generated := generatedCaskFixture(t, c, d.repository, "v#{version}")
			entries := caskAssetPattern.FindAllStringSubmatch(generated, -1)
			if len(entries) != 3 {
				t.Fatal("setup did not produce three generated pairs")
			}
			switch mutation {
			case "production-repository":
				generated = strings.ReplaceAll(generated, d.repository, repository)
			case "foreign-tag":
				generated = strings.ReplaceAll(generated, "v#{version}/", "foreign-tag/")
			case "wrong-version":
				generated = strings.ReplaceAll(generated, `version "0.0.11"`, `version "0.0.10"`)
			case "bad-digest":
				generated = strings.Replace(generated, entries[0][1], strings.Repeat("0", 64), 1)
			case "missing-pair":
				generated = strings.Replace(generated, entries[0][0], "", 1)
			case "duplicate-pair":
				generated = strings.Replace(generated, entries[1][2], entries[0][2], 1)
			}
			write(t, "dist/homebrew/Casks/dibs.rb", generated)
			before, err := os.ReadFile("dist/dibs.rb")
			if err != nil {
				t.Fatal(err)
			}
			if err := stageUnsigned(c); err == nil {
				t.Fatal("unverified scratch cask was staged for signing")
			}
			after, err := os.ReadFile("dist/dibs.rb")
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected cask overwrote the signing stage")
			}
		})
	}
}

func TestDryCaskDoesNotCrossProductionAndScratchNamespaces(t *testing.T) {
	t.Chdir(t.TempDir())
	c := config{version: "0.0.11", sha: strings.Repeat("c", 40)}
	fixtureAssets(t, c, "dist")
	production := productionTarget(c)
	scratch, err := rehearsalTarget(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, producer := range []publicationTarget{production, scratch} {
		for _, consumer := range []publicationTarget{production, scratch} {
			c.destination = consumer
			generated := generatedCaskFixture(t, c, producer.repository, producer.tag)
			write(t, "dist/dibs.rb", generated)
			err := validateDryCask(c, "dist")
			if (err == nil) != (producer == consumer) {
				t.Fatalf("producer=%+v consumer=%+v err=%v", producer, consumer, err)
			}
		}
	}
	// Even the correct scratch repository's LOCAL canonical tag is not a
	// published payload URL and cannot be accepted after signature/readback.
	c.destination = scratch
	write(t, "dist/dibs.rb", generatedCaskFixture(t, c, scratch.repository, "v#{version}"))
	if err := validateDryCask(c, "dist"); err == nil {
		t.Fatal("scratch canonical build tag accepted as a public release")
	}
	if _, err := os.Stat("dist/rehearsal-dry-plans.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cask validation wrote downstream plans")
	}
}
