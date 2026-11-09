package repl

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/emdzej/keycloak-cli/internal/config"
)

const historyLimit = 1000

// sensitiveRe matches lines that probably carry a credential. They stay in
// the session's history but are never written to disk.
var sensitiveRe = regexp.MustCompile(`(?i)(eyJ[a-zA-Z0-9_-]{10,}\.|bearer\s|--token\b|password\s*=|secret\s*=|KCCTL_TOKEN=|client_secret)`)

type History struct {
	path    string
	entries []string
}

func historyPath() string { return filepath.Join(config.StateDir(), "history") }

func LoadHistory() *History {
	h := &History{path: historyPath()}
	f, err := os.Open(h.path)
	if err != nil {
		return h
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		// Multi-line entries are stored with escaped newlines.
		h.entries = append(h.entries, strings.ReplaceAll(sc.Text(), `\n`, "\n"))
	}
	if len(h.entries) > historyLimit {
		h.entries = h.entries[len(h.entries)-historyLimit:]
	}
	return h
}

func (h *History) Add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == line {
		return
	}
	h.entries = append(h.entries, line)
	if len(h.entries) > historyLimit {
		h.entries = h.entries[len(h.entries)-historyLimit:]
	}
	if sensitiveRe.MatchString(line) {
		return
	}
	h.save()
}

func (h *History) save() {
	var b strings.Builder
	for _, e := range h.entries {
		if sensitiveRe.MatchString(e) {
			continue
		}
		b.WriteString(strings.ReplaceAll(e, "\n", `\n`))
		b.WriteByte('\n')
	}
	_ = config.WriteFilePrivate(h.path, []byte(b.String()))
}

func (h *History) Entries() []string { return h.entries }

// Search returns entries containing q, newest first, deduplicated.
func (h *History) Search(q string) []string {
	seen := map[string]bool{}
	var out []string
	q = strings.ToLower(q)
	for i := len(h.entries) - 1; i >= 0; i-- {
		e := h.entries[i]
		if seen[e] || !strings.Contains(strings.ToLower(e), q) {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}
