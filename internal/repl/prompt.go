package repl

import (
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/output"
)

const (
	promptText   = "kcctl> "
	contText     = "     > "
	dropdownRows = 8
	debounce     = 120 * time.Millisecond
)

type outcome int

const (
	outcomeRun outcome = iota
	outcomeExit
)

// promptModel edits one line. The program ends when the line is submitted
// (or the user leaves), so a command always runs with a normal terminal.
type promptModel struct {
	tree     *cobra.Command
	complete Completer
	hist     *History
	st       output.Style
	prefix   []string // session flags; completion sees them like the child will
	buf      []rune
	cur      int
	width    int
	outcome  outcome

	// completion state
	seq       int
	comp      Completion
	compFor   string // line text the completion belongs to
	loading   bool
	sel       int
	hidden    bool
	scroll    int
	cache     map[string]Completion
	diagWord  int
	diagMsg   string
	histPos   int
	histSaved string
	searching bool
	// pendingAccept: Tab was pressed while suggestions were loading; accept
	// as soon as they arrive. tabbed: Tab on an empty line opened the list.
	pendingAccept bool
	tabbed        bool
	query         []rune
	matches       []string
	matchSel      int
}

type compMsg struct {
	seq  int
	line string
	c    Completion
}

type tickMsg struct{ seq int }

func newPrompt(tree *cobra.Command, complete Completer, h *History, st output.Style, prefix []string) *promptModel {
	return &promptModel{tree: tree, complete: complete, hist: h, st: st, prefix: prefix, cache: map[string]Completion{},
		diagWord: -1, histPos: len(h.Entries()), width: 80}
}

func (m *promptModel) Init() tea.Cmd { return m.schedule() }

func (m *promptModel) line() string { return string(m.buf) }

// schedule debounces completion so fast typing doesn't hit the server for
// every keystroke; stale answers are dropped by sequence number.
func (m *promptModel) schedule() tea.Cmd {
	m.seq++
	seq := m.seq
	m.updateDiag()
	if c, ok := m.cache[m.beforeCursor()]; ok {
		m.comp, m.compFor, m.loading = c, m.beforeCursor(), false
		m.clampSel()
		return nil
	}
	m.loading = true
	return tea.Tick(debounce, func(time.Time) tea.Msg { return tickMsg{seq: seq} })
}

func (m *promptModel) beforeCursor() string { return string(m.buf[:m.cur]) }

func (m *promptModel) fetch(seq int) tea.Cmd {
	text := m.beforeCursor()
	complete, prefix := m.complete, m.prefix
	return func() tea.Msg {
		words, _ := Split(text)
		args := Texts(words)
		toComplete := ""
		if len(args) > 0 && !endsWithSpace(text) {
			toComplete = args[len(args)-1]
			args = args[:len(args)-1]
		}
		args = stripLeadingName(args)
		full := append(append([]string{}, prefix...), args...)
		return compMsg{seq: seq, line: text, c: complete(full, toComplete)}
	}
}

func endsWithSpace(s string) bool {
	return s != "" && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t')
}

// stripLeadingName ignores a typed "kcctl" at the start of the line.
func stripLeadingName(args []string) []string {
	if len(args) > 0 && args[0] == "kcctl" {
		return args[1:]
	}
	return args
}

func (m *promptModel) updateDiag() {
	words, _ := Split(m.line())
	texts := Texts(words)
	offset := 0
	if len(texts) > 0 && texts[0] == "kcctl" {
		texts, offset = texts[1:], 1
	}
	// Don't flag the word still being typed.
	if len(texts) > 0 && !endsWithSpace(m.line()) && m.cur == len(m.buf) {
		texts = texts[:len(texts)-1]
	}
	i, msg := Diagnose(m.tree, texts)
	if i >= 0 {
		i += offset
	}
	m.diagWord, m.diagMsg = i, msg
}

func (m *promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		return m, nil
	case tickMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		return m, m.fetch(msg.seq)
	case compMsg:
		m.cache[msg.line] = msg.c
		if msg.seq != m.seq {
			return m, nil
		}
		m.comp, m.compFor, m.loading = msg.c, msg.line, false
		m.sel, m.scroll = 0, 0
		if m.pendingAccept {
			m.pendingAccept = false
			if m.accept() {
				return m, m.schedule()
			}
		}
		return m, nil
	case tea.KeyMsg:
		if m.searching {
			return m.updateSearch(msg)
		}
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *promptModel) updateKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyCtrlD:
		if len(m.buf) == 0 {
			m.outcome = outcomeExit
			return m, tea.Quit
		}
		if m.cur < len(m.buf) {
			m.buf = append(m.buf[:m.cur], m.buf[m.cur+1:]...)
			return m, m.schedule()
		}
		return m, nil
	case tea.KeyCtrlC:
		if len(m.buf) == 0 {
			m.outcome = outcomeExit
			return m, tea.Quit
		}
		m.setLine("")
		return m, m.schedule()
	case tea.KeyEnter:
		if k.Alt {
			m.insert("\n")
			return m, m.schedule()
		}
		if _, incomplete := Split(m.line()); incomplete {
			m.insert("\n")
			return m, m.schedule()
		}
		m.outcome = outcomeRun
		m.comp = Completion{}
		m.diagMsg = ""
		return m, tea.Quit
	case tea.KeyTab, tea.KeyRight:
		if k.Type == tea.KeyRight && m.cur < len(m.buf) {
			m.cur++
			return m, m.schedule()
		}
		m.hidden = false
		if len(m.buf) == 0 && !m.tabbed {
			m.tabbed = true
			return m, nil
		}
		if m.accept() {
			return m, m.schedule()
		}
		if m.loading {
			m.pendingAccept = true
		}
		return m, nil
	case tea.KeyShiftTab:
		m.moveSel(-1)
		return m, nil
	case tea.KeyUp:
		if m.dropdownVisible() {
			m.moveSel(-1)
			return m, nil
		}
		m.historyStep(-1)
		return m, m.schedule()
	case tea.KeyDown:
		if m.dropdownVisible() {
			m.moveSel(1)
			return m, nil
		}
		m.historyStep(1)
		return m, m.schedule()
	case tea.KeyCtrlP:
		m.historyStep(-1)
		return m, m.schedule()
	case tea.KeyCtrlN:
		m.historyStep(1)
		return m, m.schedule()
	case tea.KeyPgUp:
		m.moveSel(-dropdownRows)
		return m, nil
	case tea.KeyPgDown:
		m.moveSel(dropdownRows)
		return m, nil
	case tea.KeyEsc:
		m.hidden = true
		return m, nil
	case tea.KeyCtrlR:
		m.searching, m.query, m.matchSel = true, nil, 0
		m.matches = m.hist.Search("")
		return m, nil
	case tea.KeyLeft:
		if m.cur > 0 {
			m.cur--
		}
		return m, m.schedule()
	case tea.KeyHome, tea.KeyCtrlA:
		m.cur = 0
		return m, m.schedule()
	case tea.KeyEnd, tea.KeyCtrlE:
		m.cur = len(m.buf)
		return m, m.schedule()
	case tea.KeyBackspace:
		if m.cur > 0 {
			m.buf = append(m.buf[:m.cur-1], m.buf[m.cur:]...)
			m.cur--
		}
		m.hidden = false
		return m, m.schedule()
	case tea.KeyDelete:
		if m.cur < len(m.buf) {
			m.buf = append(m.buf[:m.cur], m.buf[m.cur+1:]...)
		}
		return m, m.schedule()
	case tea.KeyCtrlW:
		i := m.cur
		for i > 0 && m.buf[i-1] == ' ' {
			i--
		}
		for i > 0 && m.buf[i-1] != ' ' {
			i--
		}
		m.buf = append(m.buf[:i], m.buf[m.cur:]...)
		m.cur = i
		return m, m.schedule()
	case tea.KeyCtrlU:
		m.buf = append([]rune{}, m.buf[m.cur:]...)
		m.cur = 0
		return m, m.schedule()
	case tea.KeyCtrlK:
		m.buf = m.buf[:m.cur]
		return m, m.schedule()
	case tea.KeyCtrlL:
		return m, tea.ClearScreen
	case tea.KeySpace:
		m.insert(" ")
		m.hidden = false
		return m, m.schedule()
	case tea.KeyRunes:
		m.insert(string(k.Runes))
		m.hidden = false
		return m, m.schedule()
	}
	return m, nil
}

func (m *promptModel) updateSearch(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc, tea.KeyCtrlC, tea.KeyCtrlG:
		m.searching = false
		return m, nil
	case tea.KeyEnter, tea.KeyTab:
		if m.matchSel < len(m.matches) {
			m.setLine(m.matches[m.matchSel])
		}
		m.searching = false
		return m, m.schedule()
	case tea.KeyUp, tea.KeyCtrlR, tea.KeyCtrlP:
		if m.matchSel < len(m.matches)-1 {
			m.matchSel++
		}
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		if m.matchSel > 0 {
			m.matchSel--
		}
		return m, nil
	case tea.KeyBackspace:
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
		}
	case tea.KeyRunes, tea.KeySpace:
		m.query = append(m.query, k.Runes...)
		if k.Type == tea.KeySpace {
			m.query = append(m.query, ' ')
		}
	default:
		return m, nil
	}
	m.matches = m.hist.Search(string(m.query))
	m.matchSel = 0
	return m, nil
}

func (m *promptModel) insert(s string) {
	m.pendingAccept = false
	r := []rune(s)
	nb := make([]rune, 0, len(m.buf)+len(r))
	nb = append(nb, m.buf[:m.cur]...)
	nb = append(nb, r...)
	nb = append(nb, m.buf[m.cur:]...)
	m.buf = nb
	m.cur += len(r)
}

func (m *promptModel) setLine(s string) {
	m.buf = []rune(s)
	m.cur = len(m.buf)
}

func (m *promptModel) historyStep(d int) {
	e := m.hist.Entries()
	if len(e) == 0 {
		return
	}
	if m.histPos == len(e) {
		m.histSaved = m.line()
	}
	m.histPos += d
	switch {
	case m.histPos < 0:
		m.histPos = 0
	case m.histPos >= len(e):
		m.histPos = len(e)
		m.setLine(m.histSaved)
		return
	}
	m.setLine(e[m.histPos])
}

// visibleItems are completions that still match the word being typed.
func (m *promptModel) visibleItems() []Suggestion {
	if m.compFor != m.beforeCursor() {
		return nil
	}
	return m.comp.Items
}

// The list stays hidden on an empty line until Tab asks for it, so the
// prompt is quiet until there is something to complete.
func (m *promptModel) dropdownVisible() bool {
	if len(m.buf) == 0 && !m.tabbed {
		return false
	}
	return !m.hidden && len(m.visibleItems()) > 0
}

func (m *promptModel) moveSel(d int) {
	n := len(m.visibleItems())
	if n == 0 {
		return
	}
	m.sel = ((m.sel+d)%n + n) % n
	m.clampSel()
}

func (m *promptModel) clampSel() {
	n := len(m.visibleItems())
	if m.sel >= n {
		m.sel = 0
	}
	if m.sel < m.scroll {
		m.scroll = m.sel
	}
	if m.sel >= m.scroll+dropdownRows {
		m.scroll = m.sel - dropdownRows + 1
	}
}

// currentWordStart is where the word under the cursor begins.
func (m *promptModel) currentWordStart() int {
	text := m.beforeCursor()
	if endsWithSpace(text) || text == "" {
		return m.cur
	}
	words, _ := Split(text)
	if len(words) == 0 {
		return m.cur
	}
	return utf8.RuneCountInString(text[:words[len(words)-1].Start])
}

// accept replaces the current word with the selected suggestion.
func (m *promptModel) accept() bool {
	items := m.visibleItems()
	if len(items) == 0 || m.hidden {
		return false
	}
	v := items[m.sel].Value
	start := m.currentWordStart()
	ins := Quote(v)
	noSpace := m.comp.NoSpace || strings.HasSuffix(v, "=") || strings.HasSuffix(v, "/")
	if !noSpace {
		ins += " "
	}
	rest := m.buf[m.cur:]
	nb := append(append([]rune{}, m.buf[:start]...), []rune(ins)...)
	m.cur = len(nb)
	m.buf = append(nb, rest...)
	m.sel, m.scroll = 0, 0
	return true
}

func (m *promptModel) View() string {
	if m.outcome == outcomeExit {
		return ""
	}
	var b strings.Builder
	st := m.st
	if m.searching {
		b.WriteString(st.Cyan("(history search) ") + string(m.query) + "\n")
		for i, e := range m.matches {
			if i == dropdownRows {
				break
			}
			e = strings.ReplaceAll(e, "\n", " ")
			if i == m.matchSel {
				b.WriteString(st.Bold("› "+m.trim(e, 2)) + "\n")
			} else {
				b.WriteString("  " + m.trim(e, 2) + "\n")
			}
		}
		if len(m.matches) == 0 {
			b.WriteString(st.Dim("  no matching history") + "\n")
		}
		return b.String()
	}

	b.WriteString(m.renderLine())
	if m.outcome == outcomeRun && m.comp.Items == nil {
		return b.String() + "\n"
	}
	b.WriteString("\n")
	if m.dropdownVisible() {
		items := m.visibleItems()
		w := 0
		for _, it := range items {
			w = max(w, runewidth.StringWidth(it.Value))
		}
		w = min(w, 40)
		end := min(len(items), m.scroll+dropdownRows)
		for i := m.scroll; i < end; i++ {
			it := items[i]
			val := runewidth.FillRight(runewidth.Truncate(it.Value, w, "…"), w)
			line := "  " + val + "  " + st.Dim(m.trim(it.Desc, w+4))
			if i == m.sel {
				line = st.Cyan("› ") + st.Bold(val) + "  " + st.Dim(m.trim(it.Desc, w+4))
			}
			b.WriteString(line + "\n")
		}
		if len(items) > dropdownRows {
			b.WriteString(st.Dim("  "+itoa(m.sel+1)+"/"+itoa(len(items))) + "\n")
		}
	}
	switch {
	case m.diagMsg != "":
		b.WriteString(st.Red(m.diagMsg) + "\n")
	case len(m.comp.Help) > 0 && m.compFor == m.beforeCursor():
		b.WriteString(st.Yellow(strings.Join(m.comp.Help, "; ")) + "\n")
	case m.loading && m.compFor != m.beforeCursor() && m.compFor != "":
		b.WriteString(st.Dim("loading…") + "\n")
	}
	return b.String()
}

func (m *promptModel) trim(s string, used int) string {
	avail := m.width - used - 4
	if avail < 10 {
		avail = 10
	}
	return runewidth.Truncate(s, avail, "…")
}

// renderLine draws the prompt, the text (marking the diagnosed word red),
// the cursor, and the first suggestion as dim ghost text.
func (m *promptModel) renderLine() string {
	st := m.st
	text := m.line()
	words, _ := Split(text)
	var bad *Word
	if m.diagWord >= 0 && m.diagWord < len(words) {
		bad = &words[m.diagWord]
	}
	ghost := ""
	if items := m.visibleItems(); m.dropdownVisible() && m.cur == len(m.buf) {
		v := items[m.sel].Value
		typed := string(m.buf[m.currentWordStart():m.cur])
		if strings.HasPrefix(v, typed) {
			ghost = v[len(typed):]
		}
	}
	var b strings.Builder
	b.WriteString(st.Bold(promptText))
	byteCur := len(string(m.buf[:m.cur]))
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		ch := string(r)
		if r == '\n' {
			if i == byteCur {
				b.WriteString(cursor(st, " "))
			}
			b.WriteString("\n" + st.Dim(contText))
			i += size
			continue
		}
		if bad != nil && i >= bad.Start && i < bad.End {
			ch = st.Red(ch)
		}
		if i == byteCur {
			ch = cursor(st, string(r))
		}
		b.WriteString(ch)
		i += size
	}
	if byteCur == len(text) {
		if ghost != "" {
			gr := []rune(ghost)
			b.WriteString(cursor(st, string(gr[0])) + st.Dim(string(gr[1:])))
		} else {
			b.WriteString(cursor(st, " "))
		}
	}
	return b.String()
}

// cursor uses reverse video even with colors off: it is not decoration, and
// without it the position is invisible.
func cursor(_ output.Style, ch string) string {
	return "\x1b[7m" + ch + "\x1b[27m"
}
