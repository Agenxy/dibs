package selfupdate

import (
	"errors"
	"reflect"
	"strings"
)

// GuestProvisioning is literal private-handoff guidance, not an installer or
// a second release manifest. Nothing here is executed by Dibs. Placeholders
// name guest-owned paths, never issuer paths or invitation credentials.
type GuestProvisioning struct {
	Status    string                 `json:"status"`
	Rules     []string               `json:"rules"`
	Checksums string                 `json:"checksums_txt"`
	Targets   []GuestProvisionTarget `json:"targets"`
}

// GuestProvisionTarget holds one published target's literal steps and MCP entry.
type GuestProvisionTarget struct {
	OS    string        `json:"goos"`
	Arch  string        `json:"goarch"`
	Steps []GuestStep   `json:"steps"`
	MCP   GuestMCPEntry `json:"mcp_stdio"`
}

// GuestStep describes one native command and an optional explicit stdout file.
type GuestStep struct {
	Purpose    string   `json:"purpose"`
	Cwd        string   `json:"working_directory,omitempty"`
	Command    string   `json:"command"`
	Argv       []string `json:"argv"`
	StdoutFile string   `json:"stdout_file,omitempty"`
}

// GuestMCPEntry is mergeable configuration, never an instruction to launch.
type GuestMCPEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Provisioning may be offered only from the admitted immutable snapshot. A
// zero snapshot is refused. The export and its metadata still say INCOMPLETE:
// publishing supporting artifacts and measuring a harness are separate acts.
func (s GuestReleaseSnapshot) Provisioning() (GuestProvisioning, error) {
	return renderGuestProvisioning(s.Metadata())
}

// Validate checks a received handoff's canonical rendering, NOT its signature.
// Only the authenticated private handoff vouches for these public digests;
// arbitrary JSON cannot become a GuestReleaseSnapshot or issuer evidence.
func (p GuestProvisioning) Validate(m GuestReleaseMetadata) error {
	want, err := renderGuestProvisioning(m)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(p, want) {
		return errors.New("guest provisioning differs from its exact release projection; " +
			"ask the issuer for the original private export")
	}
	return nil
}

func renderGuestProvisioning(m GuestReleaseMetadata) (GuestProvisioning, error) {
	if err := m.Validate(); err != nil {
		return GuestProvisioning{}, err
	}
	rel, err := ReleaseForTag(m.Tag)
	if err != nil {
		return GuestProvisioning{}, err
	}
	p := GuestProvisioning{Status: "INCOMPLETE", Rules: []string{
		"These are discrete steps, not a script. Stop on every nonzero exit, missing/mismatched checksum " +
			"or unavailable primitive; never continue to extraction or use after a failed check.",
		"Choose exactly your OS/architecture. Only darwin/arm64, linux/amd64 and linux/arm64 are published; " +
			"Windows and macOS Intel are unsupported. Do not run an image for another target.",
		"Replace placeholders with clean absolute guest-owned paths, without quotes or newlines. " +
			"<absolute-version-parent> is an existing private (0700), non-symlink, user-writable directory " +
			"ending in /" + m.Tag + "; never a system directory or PATH replacement. Keep prior versions recoverable.",
		"Use the directory returned by mktemp for <staging>; it is exclusive and on the installation filesystem. " +
			"Save checksums_txt from THIS private handoff as <staging>/checksums.txt verbatim. " +
			"Do not fetch another checksum file as authority.",
		"The archive is issuer-vouched signed release bytes only after its hash check succeeds. " +
			"Stdout extraction writes no archive paths; the executable hash must then pass before chmod or installation. " +
			"No installer, cosign, sudo, dibd, presence service or hook is needed or installed.",
		"Release download uses public HTTPS, no invitation bearer, guest CA or shared runtime transport. " +
			"Keep the private recipe separately in an owned 0700 directory as a 0600 file. " +
			"Never disclose its credentials in commands or logs.",
		"Merge the selected mcp_stdio command/args into your harness configuration yourself; " +
			"do not overwrite existing configuration or start a session here. No extra-CA environment is needed. " +
			"Set an adequate startup timeout only where the harness supports it.",
		"INCOMPLETE: instructions do not prove installed-harness or WAN support. A noexec/read-only filesystem, " +
			"unavailable download/hash/archive primitive, container IPv6 or network policy can prevent use; " +
			"report the actual boundary rather than bypass it.",
	}}
	// Fixed target ordering makes projection stable even when received metadata
	// lists assets in another order. Every value was validated before rendering.
	for _, target := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		var a GuestAsset
		for _, asset := range m.Assets {
			if asset.OS == target[0] && asset.Arch == target[1] {
				a = asset
			}
		}
		archive, _ := ArchiveName(rel.Version, a.OS, a.Arch)
		member, _ := GuestMemberName(a.OS, a.Arch)
		archiveLine := a.ArchiveSHA + "  " + archive
		memberLine := a.BinarySHA + "  " + member
		p.Checksums += archiveLine + "\n" + memberLine + "\n"
		platform := a.OS + "_" + a.Arch
		hash := "sha256sum -c"
		if a.OS == "darwin" {
			hash = "shasum -a 256 -c"
		}
		p.Targets = append(p.Targets, GuestProvisionTarget{
			OS: a.OS, Arch: a.Arch,
			Steps: []GuestStep{
				guestLiteralStep("Create exclusive staging; retain the printed absolute path", "", "",
					"mktemp", "-d", "<absolute-version-parent>/."+platform+".XXXXXXXX"),
				guestLiteralStep("Download public release archive, bounded to 128 MiB and 120 seconds", "<staging>", "",
					"curl", "--disable", "--fail", "--location", "--proto", "=https", "--proto-redir", "=https",
					"--noproxy", "*", "--max-time", "120", "--max-filesize", "134217728", "--output", archive, a.URL),
				guestLiteralStep("Select exactly the archive checksum from the private handoff", "<staging>", "archive.sha256",
					"grep", "-F", "-x", archiveLine, "checksums.txt"),
				guestLiteralStep("Verify archive BEFORE extraction; a missing line is failure", "<staging>", "",
					append(strings.Fields(hash), "archive.sha256")...),
				guestLiteralStep("Create only explicit staging paths, never archive-provided paths", "<staging>", "",
					"mkdir", "-m", "700", "members", "members/"+platform),
				guestLiteralStep("Stream only the named executable to our staging file", "<staging>", member,
					"tar", "-xOzf", archive, "--", "dibs"),
				guestLiteralStep("Select exactly the executable checksum from the private handoff", "<staging>", "member.sha256",
					"grep", "-F", "-x", memberLine, "checksums.txt"),
				guestLiteralStep("Verify executable BEFORE chmod or installation", "<staging>", "",
					append(strings.Fields(hash), "member.sha256")...),
				guestLiteralStep("Make only the verified executable runnable", "<staging>", "", "chmod", "700", member),
				guestLiteralStep("Create exclusive versioned target directory; existing means STOP, not overwrite", "", "",
					"mkdir", "-m", "700", "<absolute-version-parent>/"+platform),
				guestLiteralStep("Atomically publish the verified executable without replacing any existing file", "", "",
					"ln", "<staging>/"+member, "<absolute-version-parent>/"+platform+"/dibs"),
			},
			MCP: GuestMCPEntry{
				Command: "<absolute-version-parent>/" + platform + "/dibs",
				Args:    []string{"mcp-stdio", "--guest", "<absolute-private-recipe-file>"},
			},
		})
	}
	return p, nil
}

// The readable command and argument vector come from one definition. The
// vector lets an agent use its native process/file primitives without a shell;
// StdoutFile is a chosen output path, never an archive-derived write target.
func guestLiteralStep(purpose, cwd, output string, argv ...string) GuestStep {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = guestLiteralArgument(arg)
	}
	command := strings.Join(quoted, " ")
	if output != "" {
		command += " > " + guestLiteralArgument(output)
	}
	return GuestStep{Purpose: purpose, Cwd: cwd, Command: command, Argv: argv, StdoutFile: output}
}

func guestLiteralArgument(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\r\n'\"<>*$`\\;&|()?![]{}") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}
