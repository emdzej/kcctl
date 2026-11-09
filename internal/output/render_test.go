package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/emdzej/kcctl/internal/errs"
)

func printer(format string) (*Printer, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &Printer{Format: format, Out: &out, Err: &errb}, &out, &errb
}

var users = []any{
	map[string]any{"id": "1", "username": "alice", "attributes": map[string]any{"dept": []any{"sales"}}},
	map[string]any{"id": "2", "username": "bob"},
}

func TestTableUsesColumns(t *testing.T) {
	p, out, _ := printer("")
	if err := p.Print(users, []Column{Col("username", ".username"), Col("dept", ".attributes.dept[0]")}); err != nil {
		t.Fatal(err)
	}
	want := "USERNAME   DEPT\nalice      sales\nbob\n"
	if out.String() != want {
		t.Errorf("got\n%q\nwant\n%q", out.String(), want)
	}
}

func TestJSONEnvelope(t *testing.T) {
	p, out, _ := printer("json")
	_ = p.Print(users[1], nil)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env["error"] != nil || env["data"].(map[string]any)["username"] != "bob" {
		t.Errorf("envelope = %v", env)
	}
}

func TestErrorEnvelopeAndReport(t *testing.T) {
	p, out, errb := printer("json")
	p.Error(errs.NotFound("no user named 'x'").WithHint("try again"))
	if !strings.Contains(out.String(), `"code": "not_found"`) {
		t.Errorf("stdout = %s", out.String())
	}
	if !strings.Contains(errb.String(), "error: no user named 'x'") || !strings.Contains(errb.String(), "Hint: try again") {
		t.Errorf("stderr = %s", errb.String())
	}
}

func TestJSONPathAndCustomColumns(t *testing.T) {
	p, out, _ := printer("jsonpath={$[*].username}")
	_ = p.Print(users, nil)
	if out.String() != "alice\nbob\n" {
		t.Errorf("jsonpath = %q", out.String())
	}
	p, out, _ = printer("custom-columns=ID:.id,NAME:.username")
	_ = p.Print(users, nil)
	if !strings.HasPrefix(out.String(), "ID   NAME\n1    alice") {
		t.Errorf("custom-columns = %q", out.String())
	}
}

func TestValidateFormat(t *testing.T) {
	for _, ok := range []string{"", "json", "yaml", "table", "vtable", "jsonpath=$.id", "jsonpath:.id", "custom-columns=A:.a"} {
		if err := ValidateFormat(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"xml", "jsonpath=", "custom-columns=nocolon"} {
		if err := ValidateFormat(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestObjectView(t *testing.T) {
	p, out, _ := printer("")
	_ = p.Print(users[0], nil)
	s := out.String()
	if !strings.HasPrefix(s, "id") || !strings.Contains(s, "attributes:\n  dept  sales") {
		t.Errorf("object view:\n%s", s)
	}
}

func TestObjectViewSkipsEmptyContainers(t *testing.T) {
	p, out, _ := printer("")
	_ = p.Print(map[string]any{"name": "staff", "attributes": map[string]any{}, "subGroups": []any{}}, nil)
	if s := out.String(); strings.Contains(s, "attributes") || strings.Contains(s, "subGroups") {
		t.Errorf("empty containers shown:\n%s", s)
	}
}
