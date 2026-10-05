package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Drives execute -> fullPublication -> the SHARED publisher. Only subprocess
// effects are fixtures; neither the old lookup nor receipt/outcome decisions
// are substituted. Controlled cosign verdicts are wiring, not crypto proof.
func TestFullPublicationTraversesDraftDoorsAndDiscoveryOnlyNegativeControl(t *testing.T) {
	for _, scenario := range []string{"negative", "corrected", "evidence-collision"} {
		t.Run(scenario, func(t *testing.T) {
			negative, collision := scenario == "negative", scenario == "evidence-collision"
			c := fixture(t)
			c.target, c.phase, c.negativeControl = "rehearsal", "full-publication", negative
			old := rehearsalRepository
			rehearsalRepository = "Agenxy/dibs-private-test-fixture"
			t.Cleanup(func() { rehearsalRepository = old })
			d, err := rehearsalTarget(c)
			if err != nil {
				t.Fatal(err)
			}
			git(t, "tag", "-a", d.tag, c.sha, "-m", "private rehearsal fixture")
			git(t, "push", "origin", "refs/tags/"+d.tag)
			t.Setenv("GITHUB_REPOSITORY", d.repository)
			t.Setenv("GITHUB_REF", "refs/tags/"+d.tag)
			t.Setenv("GITHUB_SHA", c.sha)
			t.Setenv("RUNNER_TEMP", t.TempDir())
			for _, name := range []string{"DIBS_SIGNING_P12", "DIBS_SIGNING_P12_PASSWORD", "DIBS_SIGNING_KEYCHAIN", "DIBS_CODESIGN_IDENTITY", "HOMEBREW_TAP_DEPLOY_KEY"} {
				t.Setenv(name, "")
			}
			write(t, "server.json", `{"name":"io.github.Agenxy/dibs","version":"`+c.version+`"}`)
			// The generated manifest above is part of the candidate, not an
			// untracked substitution hidden from the source provenance fixture.
			git(t, "add", "server.json")
			git(t, "commit", "-m", "registry fixture")
			c.sha = git(t, "rev-parse", "HEAD")
			c.workflowSHA = c.sha
			d, err = rehearsalTarget(c)
			if err != nil {
				t.Fatal(err)
			}
			git(t, "tag", "-a", d.tag, c.sha, "-m", "private rehearsal fixture")
			git(t, "push", "origin", "refs/tags/"+d.tag)
			t.Setenv("GITHUB_REF", "refs/tags/"+d.tag)
			t.Setenv("GITHUB_SHA", c.sha)
			var current *releaseStatus
			var evidence *releaseStatus
			proofConfig := c
			proofConfig.publicationRun = c.runID
			proofTag := evidenceTag(proofConfig, c.attempt)
			if collision {
				evidence = &releaseStatus{ID: 790, Tag: proofTag, Draft: true}
			}
			var writes []string
			var builds, signs, verifications int
			run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
				if name == "git" {
					return command(ctx, env, name, args...)
				}
				if name == "go" {
					return nil, nil
				}
				if name == "goreleaser" {
					builds++
					if strings.Join(args, " ") != "release --clean --skip=sign,publish,announce" || len(env) != 2 || env[1] != "GORELEASER_CURRENT_TAG=v"+c.version {
						t.Fatalf("canonical build variant changed: %v %v", env, args)
					}
					fixtureAssets(t, c, "dist")
					bundleSum, err := digest("dist/dibs.mcpb")
					if err != nil {
						t.Fatal(err)
					}
					write(t, "dist/dibs.mcpb.sha256", bundleSum+"  dibs.mcpb\n")
					var body strings.Builder
					fmt.Fprintf(&body, "cask \"dibs\" do\n version \"%s\"\n", c.version)
					for _, asset := range assets(c.version) {
						if !strings.HasSuffix(asset, ".tar.gz") {
							continue
						}
						sum, err := digest(filepath.Join("dist", asset))
						if err != nil {
							t.Fatal(err)
						}
						// Measured on the real scratch run: GoReleaser derives the
						// repository from the job, but still uses the canonical tag.
						fmt.Fprintf(&body, " sha256 \"%s\"\n url \"https://github.com/%s/releases/download/v#{version}/%s\"\n", sum, d.repository, strings.ReplaceAll(asset, c.version, "#{version}"))
					}
					body.WriteString("end\n")
					write(t, "dist/homebrew/Casks/dibs.rb", body.String())
					// GoReleaser signs all generated archive member entries, not
					// the separately copied cask until stageUnsigned runs.
					write(t, "dist/checksums.txt", "")
					return nil, nil
				}
				if name == "cosign" {
					if args[0] == "sign-blob" {
						signs++
						write(t, args[3], "fixture signature")
						return nil, nil
					}
					if args[0] == "verify-blob" && len(args) == 10 && args[7] == d.identity() {
						verifications++
						return nil, nil
					}
					t.Fatalf("incorrect scratch signature operation: %v", args)
				}
				if name == "gh" && len(args) == 2 && args[0] == "api" {
					if args[1] == "repos/"+d.repository+"/releases?per_page=100&page=1" {
						list := []releaseStatus{}
						if current != nil {
							list = append(list, *current)
						}
						if evidence != nil {
							list = append(list, *evidence)
						}
						return json.Marshal(list)
					}
					if negative && args[1] == "repos/"+d.repository+"/releases/tags/"+d.tag {
						return nil, errors.New("HTTP 404: get-by-tag omits draft")
					}
				}
				if name == "gh" && len(args) == 6 && args[0] == "api" && args[1] == "repos/"+d.repository+"/git/refs" {
					if args[3] != "ref=refs/tags/"+proofTag || args[5] != "sha="+c.sha {
						t.Fatalf("evidence tag writer escaped closed target: %v", args)
					}
					return nil, nil
				}
				if name == "gh" && len(args) >= 5 && args[0] == "release" && args[2] == proofTag && args[4] == d.repository {
					switch args[1] {
					case "create":
						if current == nil || current.Draft || !current.Immutable || evidence != nil {
							t.Fatal("evidence created before immutable payload or reused")
						}
						evidence = &releaseStatus{ID: 790, Tag: proofTag, Draft: true}
						writes = append(writes, "create-evidence")
						return nil, nil
					case "upload":
						if evidence == nil || !evidence.Draft || strings.Contains(strings.Join(args, " "), "--clobber") {
							t.Fatal("overwriting evidence")
						}
						evidence.Assets = []struct{ Name string }{{publicationFile}, {publicationBundle}}
						writes = append(writes, "upload-evidence")
						return nil, nil
					case "download":
						for _, name := range []string{publicationFile, publicationBundle} {
							data, err := os.ReadFile(filepath.Join(os.Getenv("RUNNER_TEMP"), name))
							if err != nil {
								t.Fatal(err)
							}
							write(t, filepath.Join(args[6], name), string(data))
						}
						return nil, nil
					case "edit":
						evidence.Draft = false
						evidence.Immutable = true
						writes = append(writes, "publish-evidence")
						return nil, nil
					}
				}
				if name == "gh" && len(args) >= 5 && args[0] == "release" && args[2] == d.tag && args[4] == d.repository {
					switch args[1] {
					case "create":
						if current != nil {
							t.Fatal("created duplicate draft")
						}
						if !strings.Contains(strings.Join(args, " "), rehearsalWarning) {
							t.Fatal("scratch release lacks visible warning")
						}
						current = &releaseStatus{ID: 789, Tag: d.tag, Draft: true}
						writes = append(writes, "create")
						return nil, nil
					case "upload":
						if current == nil || !current.Draft {
							t.Fatal("uploaded into public/absent release")
						}
						for _, asset := range assets(c.version) {
							current.Assets = append(current.Assets, struct{ Name string }{asset})
						}
						writes = append(writes, "upload")
						return nil, nil
					case "download":
						copyFixture(t, "dist", args[6], c)
						return nil, nil
					case "edit":
						current.Draft = false
						current.Immutable = true
						writes = append(writes, "publish")
						return nil, nil
					}
				}
				t.Fatalf("unexpected full-publication effect: %s %v", name, args)
				return nil, nil
			}
			err = execute(context.Background(), c, run)
			wantSigns := 2
			if negative || collision {
				wantSigns = 1
			}
			if builds != 1 || signs != wantSigns || verifications < 1 {
				t.Fatalf("unexpected build/sign/verify count: build=%d sign=%d verify=%d err=%v", builds, signs, verifications, err)
			}
			switch {
			case negative:
				if !errors.Is(err, errDraftDisappeared) || !strings.Contains(err.Error(), "CONFIRMED discovery-only") {
					t.Fatalf("wrong negative cause: %v", err)
				}
				if strings.Join(writes, ",") != "create" || current == nil || !current.Draft || len(current.Assets) != 0 {
					t.Fatalf("negative control mutated assets/publication: %v %+v", writes, current)
				}
				if _, err = os.Stat(filepath.Join(os.Getenv("RUNNER_TEMP"), publicationFile)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("negative control wrote eligible evidence")
				}
			case collision:
				if err == nil || !strings.Contains(err.Error(), "retry collided") {
					t.Fatalf("existing evidence was not refused: %v", err)
				}
				if strings.Join(writes, ",") != "create,upload,publish" || evidence == nil || !evidence.Draft || len(evidence.Assets) != 0 {
					t.Fatalf("collision overwrote/reused evidence: %v %+v", writes, evidence)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if strings.Join(writes, ",") != "create,upload,publish,create-evidence,upload-evidence,publish-evidence" {
					t.Fatalf("public retry wrote or missing doors: %v", writes)
				}
				data, err := os.ReadFile(filepath.Join(os.Getenv("RUNNER_TEMP"), publicationFile))
				if err != nil {
					t.Fatal(err)
				}
				var proof publicationProof
				if err = json.Unmarshal(data, &proof); err != nil {
					t.Fatal(err)
				}
				c.publicationRun = c.runID
				if err = validatePublication(proof, c, d, runMeta{Attempt: 1}); err != nil {
					t.Fatal(err)
				}
				if proof.Readonly == nil || !*proof.Readonly || proof.DryPlans == nil || !*proof.DryPlans {
					t.Fatal("missing retry/plan proof")
				}
			}
		})
	}
}
