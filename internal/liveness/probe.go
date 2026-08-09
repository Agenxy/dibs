package liveness

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// base is the monotonic origin for every Sample this process takes.
//
// Recorded once, at start. Go's time.Time carries a monotonic reading and
// time.Since uses it, so this difference does not advance while the machine is
// asleep — which is the entire point. It is stored explicitly rather than left
// implicit in a time.Time subtraction, because a reader has to be able to SEE
// that the sleep correction is deliberate. Getting this wrong is silent: the
// numbers stay plausible and every laptop agent looks hung.
var base = time.Now()

// Observe takes one sample of a process and the transcript it appends to.
//
// transcript may be empty, in which case only liveness and CPU are known —
// enough to tell a dead agent from a live one, not enough to tell a working one
// from a stuck one. Say so rather than guessing.
func Observe(pid int, transcript string) Sample {
	s := Sample{
		Wall:  time.Now(),
		Mono:  time.Since(base),
		Alive: New().Alive(pid),
	}
	s.CPU, s.Elapsed = processTimes(pid)
	if transcript != "" {
		if fi, err := os.Stat(transcript); err == nil {
			s.Bytes = fi.Size()
		}
		s.Tokens = Tokens(transcript)
	}
	return s
}

// processTimes reads cumulative processor time AND how long the process has
// been alive, in one call.
//
// Both together, because their RATIO is the only thing that can convict a
// stalled agent from a single observation — and asking twice would be two forks
// for one fact. Via ps, which reads the same on macOS and Linux without cgo or
// a /proc dependency.
func processTimes(pid int) (cpu, elapsed time.Duration) {
	if pid <= 0 {
		return 0, 0
	}
	// #nosec G204 -- pid is an int, so no shell metacharacter can reach argv
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "time=,etime=").Output()
	if err != nil {
		return 0, 0
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return 0, 0
	}
	// etime uses the same [[dd-]hh:]mm:ss shape as time.
	return parsePSTime(f[0]), parsePSTime(f[1])
}

// parsePSTime reads ps's cumulative-time column, which is [[dd-]hh:]mm:ss(.ff).
//
// Split out from cpuTime so the format can be tested without a process: it is
// the piece most likely to be wrong, it differs between platforms, and a silent
// zero here would make every agent look frozen.
func parsePSTime(s string) time.Duration {
	if s == "" {
		return 0
	}
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		days, _ = strconv.Atoi(d)
		s = rest
	}
	parts := strings.Split(s, ":")
	var total float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0
		}
		total = total*60 + v
	}
	return time.Duration(total*float64(time.Second)) + time.Duration(days)*24*time.Hour
}

// Tokens reads an agent's own cumulative token count out of its transcript.
//
// Both harnesses that matter write one, in different shapes, and both write it
// repeatedly as the run proceeds — so the LAST occurrence is the current total:
//
//	codex        {"type":"event_msg","payload":{"type":"token_count",
//	              "info":{"total_token_usage":{"total_tokens":N}}}}
//	claude code  {"message":{"usage":{"input_tokens":N,"output_tokens":N,...}}}
//	pi           {"type":"message","message":{"usage":{"totalTokens":N,...}}}
//
// Claude Code reports per-message usage rather than a running total, so the
// totals are accumulated here. Either way the result only has to be
// MONOTONIC — nothing compares it against a billing figure, it is compared
// against its own previous value to answer "did this move".
//
// Returns 0 when the format is unrecognised, which the classifier reads as "no
// token signal" and falls back to file growth. A wrong number would be worse
// than none: it would make a stalled agent look busy.
func Tokens(transcript string) int64 {
	f, err := os.Open(transcript) // #nosec G304 -- an operator-supplied path
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24) // transcripts carry very long lines
	var codexTotal, perMessageSum int64
	for sc.Scan() {
		line := sc.Bytes()
		// Cheap prefilter: parsing every line of a 3 MB transcript as JSON when
		// only a few carry usage is most of the cost of a sample.
		if !strings.Contains(string(line), "token") && !strings.Contains(string(line), "usage") {
			continue
		}
		var rec struct {
			Payload struct {
				Type string `json:"type"`
				Info struct {
					Total struct {
						TotalTokens int64 `json:"total_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
			Message struct {
				Usage struct {
					Input       int64 `json:"input_tokens"`
					Output      int64 `json:"output_tokens"`
					CacheRead   int64 `json:"cache_read_input_tokens"`
					CacheCreate int64 `json:"cache_creation_input_tokens"`
					// pi reports a per-message total under its own name. The
					// field sets do not overlap, so one struct reads both
					// without either harness contaminating the other's count.
					Total int64 `json:"totalTokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec.Payload.Type == "token_count" && rec.Payload.Info.Total.TotalTokens > 0 {
			codexTotal = rec.Payload.Info.Total.TotalTokens
		}
		u := rec.Message.Usage
		perMessageSum += u.Input + u.Output + u.CacheRead + u.CacheCreate + u.Total
	}
	if codexTotal > 0 {
		return codexTotal
	}
	return perMessageSum
}

// FindTranscript locates the transcript a given process is writing to.
//
// It asks the PROCESS which files it has open, rather than picking the most
// recently modified transcript on disk. That distinction is not academic: the
// recency version, run from inside a Claude Code session, discovered the
// PARENT'S own transcript — which is being appended to constantly — and
// cheerfully reported a subagent as "working" on the strength of its
// supervisor's activity. A watchdog that reports health because the watcher is
// busy is worse than no watchdog.
//
// Returns "" when the process has no recognisable transcript open, which is
// honest: the caller then has liveness and CPU, and says only what those
// support. Guessing here is how the above happened.
func FindTranscript(pid int) string {
	for _, f := range openFiles(pid) {
		if isTranscript(f) {
			return f
		}
	}
	return ""
}

// isTranscript recognises a harness session transcript by its path shape.
//
// Matched against the same locations transcriptGlobs lists, but as a predicate,
// because here the candidate comes from the process rather than from the
// filesystem — the question is "is this file one of those", not "which files
// exist".
func isTranscript(path string) bool {
	if !strings.HasSuffix(path, ".jsonl") {
		return false
	}
	for _, pat := range transcriptGlobs() {
		if ok, _ := filepath.Match(pat, path); ok {
			return true
		}
	}
	return false
}

// openFiles lists the regular files a process holds open.
//
// /proc where it exists, lsof otherwise. Both are best-effort: a failure here
// means no transcript is found, which degrades the verdict rather than
// corrupting it.
func openFiles(pid int) []string {
	if pid <= 0 {
		return nil
	}
	if entries, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd")); err == nil {
		var out []string
		for _, e := range entries {
			if target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", e.Name())); err == nil {
				out = append(out, target)
			}
		}
		return out
	}
	// -F n prints one field per line, each open name prefixed with 'n'; -a -d
	// restricts to regular file descriptors so sockets and pipes are not
	// scanned. Errors are ignored deliberately: lsof exits non-zero when some
	// descriptors are unreadable while still printing the ones that are.
	// #nosec G204 -- pid is an int, so no shell metacharacter can reach argv
	out, _ := exec.Command("lsof", "-p", strconv.Itoa(pid), "-a", "-d", "0-999", "-F", "n").Output()
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if name, ok := strings.CutPrefix(line, "n"); ok {
			files = append(files, name)
		}
	}
	return files
}

// transcriptGlobs is where the harnesses put their session transcripts.
//
// Listed rather than configured because these are facts about other people's
// software, and a wrong guess produces "" — the safe answer — rather than a
// confident reading of the wrong file.
func transcriptGlobs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl"),
		filepath.Join(home, ".claude", "projects", "*", "*.jsonl"),
		filepath.Join(home, ".pi", "agent", "sessions", "*", "*.jsonl"),
	}
}
