package interpolate

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// echoLookup renders every ref as a recognizable token so a test can see which
// refs were substituted where.
func echoLookup(r Ref) (string, error) {
	switch r.Namespace {
	case NamespaceVars:
		return "<var:" + r.Var + ">", nil
	case NamespaceRun:
		return "<run:" + r.RunKey + ">", nil
	case NamespaceComponents:
		return "<out:" + r.Component + "/" + r.Output + ">", nil
	}
	return "", fmt.Errorf("unexpected namespace %q", r.Namespace)
}

func TestParseAndRender(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"literal only", "replicas: 2\n", "replicas: 2\n"},
		{"single var", "${{ vars.CUSTOMER_ID }}", "<var:CUSTOMER_ID>"},
		{"no inner whitespace", "${{vars.CUSTOMER_ID}}", "<var:CUSTOMER_ID>"},
		{"extra inner whitespace", "${{   vars.CUSTOMER_ID   }}", "<var:CUSTOMER_ID>"},
		{"embedded in literal", "host: ${{ vars.CUSTOMER_ID }}.example.com", "host: <var:CUSTOMER_ID>.example.com"},
		{"run key", "tag: ${{ run.git_sha_short }}", "tag: <run:git_sha_short>"},
		{"multiple refs", "${{ vars.A }}-${{ run.id }}-${{ vars.B }}", "<var:A>-<run:id>-<var:B>"},
		{"components simple", "${{ components.infra.outputs.namespace }}", "<out:infra/namespace>"},
		{"components name with spaces", "${{ components.my infra.outputs.ns }}", "<out:my infra/ns>"},
		{"components name with dots splits on last .outputs.", "${{ components.a.outputs.b.outputs.key }}", "<out:a.outputs.b/key>"},
		{"escape renders literal", "tag: $${{ vars.A }}", "tag: ${{ vars.A }}"},
		{"escape next to ref", "$${{ vars.A }} ${{ vars.A }}", "${{ vars.A }} <var:A>"},
		{"helm braces untouched", "name: {{ .Release.Name }}", "name: {{ .Release.Name }}"},
		{"shell var untouched", "name: $CUSTOMER_ID", "name: $CUSTOMER_ID"},
		{"lone closing braces untouched", "a }} b", "a }} b"},
		{"multiline values", "a: ${{ vars.A }}\nb:\n  c: ${{ run.action }}\n", "a: <var:A>\nb:\n  c: <run:action>\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			tpl, err := Parse(c.in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.in, err)
			}
			got, err := tpl.Render(echoLookup)
			if err != nil {
				t.Fatalf("Render(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("Render(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		wantMsg string // substring the error must carry, so messages stay actionable
	}{
		{"unterminated", "tag: ${{ vars.A", "unterminated"},
		{"unterminated suggests escape", "literal ${{ here", "$${{"},
		{"empty expr", "${{ }}", "empty reference"},
		{"empty braces", "${{}}", "empty reference"},
		{"unknown namespace", "${{ env.HOME }}", `unknown namespace "env"`},
		{"vars with no name", "${{ vars }}", "vars takes exactly one"},
		{"vars trailing dot", "${{ vars. }}", "vars takes exactly one"},
		{"vars extra segment", "${{ vars.A.B }}", "vars takes exactly one"},
		{"vars bad name", "${{ vars.9LIVES }}", "vars takes exactly one"},
		{"vars name with space", "${{ vars.MY VAR }}", "vars takes exactly one"},
		{"run with no key", "${{ run }}", "run takes exactly one"},
		{"run extra segment", "${{ run.git.sha }}", "run takes exactly one"},
		{"components without outputs", "${{ components.infra }}", "components.<name>.outputs.<key>"},
		{"components missing key", "${{ components.infra.outputs. }}", "components.<name>.outputs.<key>"},
		{"components missing name", "${{ components..outputs.key }}", "components.<name>.outputs.<key>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(c.in)
			if err == nil {
				t.Fatalf("Parse(%q): expected error", c.in)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("Parse(%q) error %q does not contain %q", c.in, err, c.wantMsg)
			}
		})
	}
}

func TestRefs(t *testing.T) {
	t.Parallel()
	tpl, err := Parse("h: ${{ vars.A }} t: ${{ run.git_sha }} n: ${{ components.infra.outputs.ns }}")
	if err != nil {
		t.Fatal(err)
	}
	got := tpl.Refs()
	want := []Ref{
		{Namespace: NamespaceVars, Var: "A", Raw: "${{ vars.A }}"},
		{Namespace: NamespaceRun, RunKey: "git_sha", Raw: "${{ run.git_sha }}"},
		{Namespace: NamespaceComponents, Component: "infra", Output: "ns", Raw: "${{ components.infra.outputs.ns }}"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Refs() = %#v, want %#v", got, want)
	}

	// A ref-free template (escapes included) reports no refs.
	plain, err := Parse("a: $${{ vars.A }}")
	if err != nil {
		t.Fatal(err)
	}
	if refs := plain.Refs(); len(refs) != 0 {
		t.Errorf("escaped-only template Refs() = %v, want none", refs)
	}
}

func TestRenderLookupError(t *testing.T) {
	t.Parallel()
	tpl, err := Parse("a: ${{ vars.OK }}\nb: ${{ vars.MISSING }}")
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("${{ vars.MISSING }}: not defined")
	_, err = tpl.Render(func(r Ref) (string, error) {
		if r.Var == "MISSING" {
			return "", boom
		}
		return "ok", nil
	})
	// The lookup error must come back as-is — it owns the actionable message.
	if !errors.Is(err, boom) {
		t.Fatalf("Render error = %v, want the lookup error", err)
	}
}

func TestRenderRawCarriesAuthoredText(t *testing.T) {
	t.Parallel()
	tpl, err := Parse("${{vars.A}} and ${{  vars.A }}")
	if err != nil {
		t.Fatal(err)
	}
	refs := tpl.Refs()
	if len(refs) != 2 || refs[0].Raw != "${{vars.A}}" || refs[1].Raw != "${{  vars.A }}" {
		t.Errorf("Raw must preserve the authored spelling, got %#v", refs)
	}
}
