package build

import "testing"

// `go install …@latest` passes no ldflags, so the binary reported 0.0.0-dev —
// a string that means "somebody's working tree" — for something the module
// proxy built from a tag. README documents `go install` as a supported route,
// so that was a supported build telling every surface, and every agent on
// connect, something false about itself.
func TestVersionPrefersTheMostSpecificSourceAvailable(t *testing.T) {
	for _, tc := range []struct{ name, stamped, module, want string }{
		{"release build wins over everything", "0.0.0", "v0.0.0", "0.0.0"},
		{"release build wins even against a stale module version", "1.2.3", "v0.0.0", "1.2.3"},
		{"go install falls back to the module version", devVersion, "v0.0.0", "0.0.0"},
		{"and strips the v Go puts on it", devVersion, "v1.4.2", "1.4.2"},
		{"a local build in the module stays honest", devVersion, "(devel)", devVersion},
		{"so does a build with no module info at all", devVersion, "", devVersion},
	} {
		if got := resolve(tc.stamped, tc.module); got != tc.want {
			t.Errorf("%s: resolve(%q, %q) = %q, want %q",
				tc.name, tc.stamped, tc.module, got, tc.want)
		}
	}
}
