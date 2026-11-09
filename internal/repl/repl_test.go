package repl

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/output"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		in         string
		want       []string
		incomplete bool
	}{
		{`user get alice`, []string{"user", "get", "alice"}, false},
		{`realm create 'my realm' -s "a=b c"`, []string{"realm", "create", "my realm", "-s", "a=b c"}, false},
		{`a\ b c`, []string{"a b", "c"}, false},
		{`user create 'open`, []string{"user", "create", "open"}, true},
		{`user list \`, []string{"user", "list"}, true},
		{`x "a\"b"`, []string{"x", `a"b`}, false},
	}
	for _, c := range cases {
		ws, inc := Split(c.in)
		if got := Texts(ws); !reflect.DeepEqual(got, c.want) || inc != c.incomplete {
			t.Errorf("Split(%q) = %q,%v want %q,%v", c.in, got, inc, c.want, c.incomplete)
		}
	}
}

func TestQuoteRoundTrips(t *testing.T) {
	for _, s := range []string{"plain", "with space", "it's", "/group/path", ""} {
		ws, _ := Split(Quote(s))
		if len(ws) != 1 || ws[0].Text != s {
			t.Errorf("Quote(%q) = %q does not round-trip", s, Quote(s))
		}
	}
}

func tree() *cobra.Command {
	root := &cobra.Command{Use: "kcctl"}
	root.PersistentFlags().StringP("realm", "r", "", "")
	root.PersistentFlags().CountP("verbose", "v", "")
	user := &cobra.Command{Use: "user", Aliases: []string{"users"}}
	get := &cobra.Command{Use: "get", Run: func(*cobra.Command, []string) {},
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return cobra.AppendActiveHelp([]string{"alice\tid-1", "bob\tid-2"}, "loaded"), cobra.ShellCompDirectiveNoFileComp
		}}
	get.Flags().Bool("full", false, "")
	user.AddCommand(get, &cobra.Command{Use: "list", Run: func(*cobra.Command, []string) {}})
	root.AddCommand(user)
	return root
}

func TestCompleteUsesCobraTree(t *testing.T) {
	complete := TreeCompleter(tree)
	c := complete([]string{"user", "get"}, "")
	want := []Suggestion{{"alice", "id-1"}, {"bob", "id-2"}}
	if !reflect.DeepEqual(c.Items, want) {
		t.Errorf("items = %v", c.Items)
	}
	if len(c.Help) != 1 || c.Help[0] != "loaded" {
		t.Errorf("help = %v", c.Help)
	}
	c = complete([]string{"user"}, "l")
	if len(c.Items) != 1 || c.Items[0].Value != "list" {
		t.Errorf("subcommands = %v", c.Items)
	}
}

func TestDiagnose(t *testing.T) {
	cases := []struct {
		words []string
		idx   int
	}{
		{[]string{"user", "get", "alice"}, -1},
		{[]string{"users", "list"}, -1},
		{[]string{"-r", "acme", "user", "list"}, -1},
		{[]string{"user", "get", "-vv", "--full", "x"}, -1},
		{[]string{"usr"}, 0},
		{[]string{"user", "lst"}, 1},
		{[]string{"user", "get", "--nope"}, 2},
		{[]string{"help", "user"}, -1},
	}
	for _, c := range cases {
		if i, msg := Diagnose(tree(), c.words); i != c.idx {
			t.Errorf("Diagnose(%v) = %d (%s), want %d", c.words, i, msg, c.idx)
		}
	}
}

func TestSensitiveLinesStayOffDisk(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	h := LoadHistory()
	h.Add("user list")
	h.Add("api GET users --token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.x")
	if len(h.Entries()) != 2 {
		t.Fatalf("session history = %v", h.Entries())
	}
	if got := LoadHistory().Entries(); !reflect.DeepEqual(got, []string{"user list"}) {
		t.Errorf("on disk = %v", got)
	}
}

// While editing, a mistyped command is marked and explained even when there
// are no suggestions to show.
func TestViewExplainsUnknownCommand(t *testing.T) {
	m := newPrompt(tree(), func([]string, string) Completion { return Completion{} }, &History{}, output.NewStyle(false), nil)
	for _, r := range "usr list" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if v := m.View(); !strings.Contains(v, "unknown command 'usr' for 'kcctl'") {
		t.Errorf("view:\n%s", v)
	}
}
