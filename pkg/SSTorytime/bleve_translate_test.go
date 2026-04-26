package SSTorytime

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestParseError_Error covers the canonical Error() formatting so callers can
// rely on the "position N" substring for UI presentation.
func TestParseError_Error(t *testing.T) {
	pe := &ParseError{Pos: 6, Message: "operator '&' missing right operand"}
	got := pe.Error()
	want := "parse error at position 6: operator '&' missing right operand"
	if got != want {
		t.Fatalf("ParseError.Error() = %q; want %q", got, want)
	}
}

// TestParseQuery_BareToken_US1 covers the US1 tokenizer subset:
// plain words → bareToken, parenthesized words → accentToken (the inner
// text only). CJK characters share the bareToken production — the
// analyzer chain (cjk bigram on text_cjk) is what splits them downstream.
func TestParseQuery_BareToken_US1(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want queryNode
	}{
		{"latin word", "running", bareToken{Text: "running"}},
		{"hanzi word", "房子", bareToken{Text: "房子"}},
		{"accent fold", "(fangzi)", accentToken{Text: "fangzi"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseQuery(tc.in)
			if err != nil {
				t.Fatalf("parseQuery(%q): unexpected error %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseQuery(%q) = %#v; want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseQuery_AtomTokenizer_US2 covers the new atom productions added by
// US2: phrase, exact-bounds, prefix-suffix, and proximity. Each row is a
// single chunk that lowers to exactly one queryNode.
func TestParseQuery_AtomTokenizer_US2(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want queryNode
	}{
		{"phrase", `"fish soup"`, phraseToken{Text: "fish soup"}},
		{"exact bang", `!A!`, exactToken{Text: "A"}},
		{"exact pipe", `|A|`, exactToken{Text: "A"}},
		{"prefix", `flo:*`, prefixToken{Stem: "flo"}},
		{"proximity adjacent", `strange<->kind`, proximity{A: "strange", B: "kind", Slop: 0}},
		{"proximity slop", `strange<2>woman`, proximity{A: "strange", B: "woman", Slop: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseQuery(tc.in)
			if err != nil {
				t.Fatalf("parseQuery(%q): unexpected error %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseQuery(%q) = %#v; want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseQuery_BinaryOps_US2 pins PostgreSQL ts_query precedence: `&`
// binds tighter than `|`, and unary `!` binds tighter than `&`.
func TestParseQuery_BinaryOps_US2(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want queryNode
	}{
		{
			"and-not",
			`brain&!notes`,
			conjunction{Children: []queryNode{
				bareToken{Text: "brain"},
				mustNot{Child: bareToken{Text: "notes"}},
			}},
		},
		{
			"or",
			`apple|pear`,
			disjunction{Children: []queryNode{
				bareToken{Text: "apple"},
				bareToken{Text: "pear"},
			}},
		},
		{
			"and-or precedence",
			`a&b|c`,
			disjunction{Children: []queryNode{
				conjunction{Children: []queryNode{
					bareToken{Text: "a"},
					bareToken{Text: "b"},
				}},
				bareToken{Text: "c"},
			}},
		},
		{
			"not binds tighter than and",
			`!a&b`,
			conjunction{Children: []queryNode{
				mustNot{Child: bareToken{Text: "a"}},
				bareToken{Text: "b"},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseQuery(tc.in)
			if err != nil {
				t.Fatalf("parseQuery(%q): unexpected error %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseQuery(%q) = %#v; want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseQuery_LiteralVsQuerified_US2 enforces FR-015a: operator-like
// characters with whitespace on either side are literal text and must not
// trigger operator parsing. The trailing-operator case (no right operand)
// surfaces a ParseError positioned just past the operator byte.
func TestParseQuery_LiteralVsQuerified_US2(t *testing.T) {
	t.Run("ampersand with whitespace is literal", func(t *testing.T) {
		got, err := parseQuery(`chemistry C & S reaction`)
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		want := disjunction{Children: []queryNode{
			bareToken{Text: "chemistry"},
			bareToken{Text: "C"},
			bareToken{Text: "&"},
			bareToken{Text: "S"},
			bareToken{Text: "reaction"},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v; want %#v", got, want)
		}
	})
	t.Run("angle-bracketed standalone is literal", func(t *testing.T) {
		got, err := parseQuery(`effect of <ions> on pH`)
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		want := disjunction{Children: []queryNode{
			bareToken{Text: "effect"},
			bareToken{Text: "of"},
			bareToken{Text: "<ions>"},
			bareToken{Text: "on"},
			bareToken{Text: "pH"},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v; want %#v", got, want)
		}
	})
	t.Run("trailing operator with no right operand", func(t *testing.T) {
		_, err := parseQuery(`brain&`)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("want *ParseError, got %v", err)
		}
		if pe.Pos != 6 {
			t.Fatalf("pe.Pos = %d; want 6", pe.Pos)
		}
		if !strings.Contains(pe.Message, "missing right operand") {
			t.Fatalf("pe.Message = %q; want 'missing right operand' substring", pe.Message)
		}
	})
}

// TestParseQuery_Malformed_US2 covers FR-015b error paths: unclosed phrase,
// negative slop, unclosed accent-fold parens.
func TestParseQuery_Malformed_US2(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"unclosed phrase", `"fish soup`},
		{"negative slop", `strange<-1>kind`},
		{"unclosed paren", `(unclosed`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseQuery(tc.in)
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("parseQuery(%q): want *ParseError, got %v", tc.in, err)
			}
		})
	}
}
