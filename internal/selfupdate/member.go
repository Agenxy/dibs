package selfupdate

import "fmt"

// GuestMemberName is the checksum key for the packaged CLI, not a downloadable
// asset or a second manifest. The signed tag supplies its version. These are
// exactly the published guest-provisioning targets; a cross-build is not one.
func GuestMemberName(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "darwin/arm64", "linux/amd64", "linux/arm64":
		return "members/" + goos + "_" + goarch + "/dibs", nil
	default:
		return "", fmt.Errorf("no published guest CLI member for %s/%s", goos, goarch)
	}
}
