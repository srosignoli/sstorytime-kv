package SSTorytime

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/blevesearch/bleve/v2"
	asciifoldingChar "github.com/blevesearch/bleve/v2/analysis/char/asciifolding"
	"github.com/blevesearch/bleve/v2/search/query"
)

// asciiFold mirrors the index-side text_raw analyzer chain
// (asciifolding char filter → lowercase) for the query side. exactToken
// and prefixToken bypass Bleve's MatchQuery analyzer pipeline, so the
// caller must apply this fold before constructing TermQuery/PrefixQuery.
var asciiFold = asciifoldingChar.New()

func foldRaw(s string) string {
	return strings.ToLower(string(asciiFold.Filter([]byte(s))))
}

// ParseError indicates malformed operator syntax in a query string.
// SearchByQuery returns *ParseError (not a wrapped error) when the input
// fails to parse. Callers can type-assert to extract the position for UI
// presentation.
type ParseError struct {
	Pos     int    // byte offset into the original query string
	Message string // human-readable description of the problem
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error at position %d: %s", e.Pos, e.Message)
}

// queryNode is the closed AST produced by the operator-grammar parser and
// consumed by the lower function. All concrete types implement qNode().
type queryNode interface{ qNode() }

// bareToken is a plain unmodified word: e.g. running.
type bareToken struct{ Text string }

// phraseToken is a double-quoted phrase: e.g. "fish soup".
type phraseToken struct{ Text string }

// exactToken is a bang- or pipe-bounded literal: e.g. !A! or |A|.
// Translated to a Term query against text_raw (lowercased on both sides).
type exactToken struct{ Text string }

// accentToken is a parenthesized accent-fold form: e.g. (fangzi).
// Lowering identical to bareToken — the analyzer chain already accent-folds.
type accentToken struct{ Text string }

// prefixToken is a wildcard-suffix form: e.g. flo:* — matches any text
// starting with the stem after lowercasing.
type prefixToken struct{ Stem string }

// proximity expresses adjacency or slop-bounded ordering between two words.
// <-> => Slop=0, <N> => Slop=N (must be ≥ 0).
type proximity struct {
	A, B string
	Slop int
}

type conjunction struct{ Children []queryNode } // a&b
type disjunction struct{ Children []queryNode } // a|b
type mustNot struct{ Child queryNode }          // !a (prefix unary)

func (bareToken) qNode()   {}
func (phraseToken) qNode() {}
func (exactToken) qNode()  {}
func (accentToken) qNode() {}
func (prefixToken) qNode() {}
func (proximity) qNode()   {}
func (conjunction) qNode() {}
func (disjunction) qNode() {}
func (mustNot) qNode()     {}

// parseQuery is the package-private entry point for the operator-grammar
// translator. It splits the input into whitespace-separated chunks (with
// `"…"` and `(…)` allowed to span whitespace), parses each chunk, and
// combines the results via top-level disjunction (FR-015 default-OR).
//
// Empty / whitespace-only input returns (nil, nil). SearchByQuery maps
// that to (nil, nil) per the contract.
//
// Operators (`& | !`) only fire in clearly-querified positions per FR-015a:
// directly adjacent to operands without surrounding whitespace, or inside
// the recognised delimiters `"…"`, `!…!`, `|…|`, `(…)`. Whitespace-padded
// operator-like text is treated as plain bareTokens.
func parseQuery(s string) (queryNode, error) {
	chunks, err := splitChunks(s)
	if err != nil {
		return nil, err
	}
	nodes := make([]queryNode, 0, len(chunks))
	for _, c := range chunks {
		node, perr := parseChunk(c.text, c.start)
		if perr != nil {
			return nil, perr
		}
		if node == nil {
			continue
		}
		nodes = append(nodes, node)
	}
	switch len(nodes) {
	case 0:
		return nil, nil
	case 1:
		return nodes[0], nil
	default:
		return disjunction{Children: nodes}, nil
	}
}

// chunkSpan is one whitespace-bounded slice of the input plus its start
// offset, used to translate intra-chunk error positions back into
// absolute query offsets.
type chunkSpan struct {
	text  string
	start int
}

func isAsciiSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// splitChunks walks the input byte-by-byte, emitting whitespace-bounded
// chunks. `"…"` and `(…)` are allowed to span whitespace so that
// `"fish soup"` and `(fang zi)` survive as one chunk apiece. Unclosed
// quote / paren spans surface a *ParseError pointing at the opening byte.
func splitChunks(s string) ([]chunkSpan, error) {
	var out []chunkSpan
	n := len(s)
	i := 0
	for i < n {
		for i < n && isAsciiSpace(s[i]) {
			i++
		}
		if i >= n {
			break
		}
		start := i
		// Phrase: scan to closing '"'.
		if s[i] == '"' {
			j := i + 1
			for j < n && s[j] != '"' {
				j++
			}
			if j >= n {
				return nil, &ParseError{Pos: start, Message: "unclosed phrase"}
			}
			i = j + 1
			out = append(out, chunkSpan{text: s[start:i], start: start})
			continue
		}
		// Accent-fold parens: scan to closing ')'. (We let
		// tokenizeChunk re-validate; this just lets parens span
		// whitespace cleanly.)
		if s[i] == '(' {
			j := i + 1
			for j < n && s[j] != ')' {
				j++
			}
			if j >= n {
				return nil, &ParseError{Pos: start, Message: "unclosed '('"}
			}
			i = j + 1
			out = append(out, chunkSpan{text: s[start:i], start: start})
			continue
		}
		// Bare chunk: consume non-whitespace.
		for i < n && !isAsciiSpace(s[i]) {
			i++
		}
		out = append(out, chunkSpan{text: s[start:i], start: start})
	}
	return out, nil
}

// parseChunk turns one chunk into a queryNode. The chunk-shape forms
// (`"…"`, `(…)`, `!…!`, `|…|`) are matched first so their interiors
// are not subject to operator parsing. Anything else falls through to
// the operator-aware tokenizer + Pratt parser.
//
// A chunk that is exactly `&` or `|` is treated as a literal bareToken
// per FR-015a — the operator interpretation requires adjacency to
// operands within the same chunk.
func parseChunk(text string, basePos int) (queryNode, error) {
	if text == "" {
		return nil, nil
	}
	// Phrase: `"…"`.
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		return phraseToken{Text: text[1 : len(text)-1]}, nil
	}
	// Accent-fold parens: `(…)`.
	if len(text) >= 2 && text[0] == '(' && text[len(text)-1] == ')' {
		inner := strings.TrimSpace(text[1 : len(text)-1])
		if inner == "" {
			return nil, &ParseError{Pos: basePos, Message: "empty accent-fold parens"}
		}
		return accentToken{Text: inner}, nil
	}
	// Exact bang: `!X!` where X has no operator chars and no inner `!`.
	if len(text) >= 3 && text[0] == '!' && text[len(text)-1] == '!' {
		body := text[1 : len(text)-1]
		if !strings.ContainsAny(body, "!&|\"()") {
			return exactToken{Text: body}, nil
		}
	}
	// Exact pipe: `|X|` where X has no operator chars and no inner `|`.
	if len(text) >= 3 && text[0] == '|' && text[len(text)-1] == '|' {
		body := text[1 : len(text)-1]
		if !strings.ContainsAny(body, "!&|\"()") {
			return exactToken{Text: body}, nil
		}
	}
	// Solo `&` / `|` / `!` chunks are literal text per FR-015a.
	if text == "&" || text == "|" || text == "!" {
		return bareToken{Text: text}, nil
	}
	return parseOpChunk(text, basePos)
}

// chunkTokKind tags each token emitted by tokenizeChunk so the Pratt
// parser (parseOR/parseAND/parseNOT) can dispatch without re-inspecting
// the source text.
type chunkTokKind int

const (
	tokAtom chunkTokKind = iota
	tokAnd
	tokOr
	tokNot
)

type chunkTok struct {
	kind chunkTokKind
	atom queryNode
	pos  int
}

// parseOpChunk runs the operator-aware tokenizer over a chunk and feeds
// the result to the Pratt parser.
func parseOpChunk(text string, basePos int) (queryNode, error) {
	toks, err := tokenizeChunk(text, basePos)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, nil
	}
	st := &tokState{toks: toks, endPos: basePos + len(text)}
	node, err := parseOr(st)
	if err != nil {
		return nil, err
	}
	if st.idx < len(st.toks) {
		t := st.toks[st.idx]
		return nil, &ParseError{Pos: t.pos, Message: "unexpected token"}
	}
	return node, nil
}

// tokenizeChunk walks one chunk, emitting atoms (bare/proximity/prefix/
// accent/phrase) and operator markers. Inner `(…)` and `"…"` are
// recognised so nested forms like `brain&(fang)` work. Operator chars
// inside a WORD never split the WORD because the WORD scanner stops on
// `& | ! " ( )` only.
func tokenizeChunk(text string, basePos int) ([]chunkTok, error) {
	out := make([]chunkTok, 0, 4)
	i := 0
	for i < len(text) {
		c := text[i]
		switch {
		case c == '&':
			out = append(out, chunkTok{kind: tokAnd, pos: basePos + i})
			i++
		case c == '|':
			out = append(out, chunkTok{kind: tokOr, pos: basePos + i})
			i++
		case c == '!':
			out = append(out, chunkTok{kind: tokNot, pos: basePos + i})
			i++
		case c == '(':
			j := strings.IndexByte(text[i+1:], ')')
			if j < 0 {
				return nil, &ParseError{Pos: basePos + i, Message: "unclosed '('"}
			}
			inner := strings.TrimSpace(text[i+1 : i+1+j])
			if inner == "" {
				return nil, &ParseError{Pos: basePos + i, Message: "empty accent-fold parens"}
			}
			out = append(out, chunkTok{kind: tokAtom, atom: accentToken{Text: inner}, pos: basePos + i})
			i = i + 1 + j + 1
		case c == ')':
			return nil, &ParseError{Pos: basePos + i, Message: "unexpected ')'"}
		case c == '"':
			j := strings.IndexByte(text[i+1:], '"')
			if j < 0 {
				return nil, &ParseError{Pos: basePos + i, Message: "unclosed phrase"}
			}
			out = append(out, chunkTok{kind: tokAtom, atom: phraseToken{Text: text[i+1 : i+1+j]}, pos: basePos + i})
			i = i + 1 + j + 1
		default:
			// WORD: read until operator/special.
			start := i
			for i < len(text) {
				b := text[i]
				if b == '&' || b == '|' || b == '!' || b == '(' || b == ')' || b == '"' {
					break
				}
				i++
			}
			if i == start {
				return nil, &ParseError{Pos: basePos + i, Message: "unexpected character"}
			}
			word := text[start:i]
			atom, err := parseWordToken(word, basePos+start)
			if err != nil {
				return nil, err
			}
			out = append(out, chunkTok{kind: tokAtom, atom: atom, pos: basePos + start})
		}
	}
	return out, nil
}

// parseWordToken interprets a WORD as proximity (`A<->B` or `A<N>B`),
// prefix (`stem:*`), or plain bareToken. The `<…>` shape only fires when
// surrounded by non-empty operands — e.g. `<ions>` standalone stays a
// bareToken because the left operand is empty.
func parseWordToken(word string, pos int) (queryNode, error) {
	if open := strings.IndexByte(word, '<'); open > 0 {
		if rel := strings.IndexByte(word[open+1:], '>'); rel >= 0 {
			close := open + 1 + rel
			if close+1 < len(word) {
				a := word[:open]
				slopText := word[open+1 : close]
				b := word[close+1:]
				if a != "" && b != "" {
					if slopText == "-" {
						return proximity{A: a, B: b, Slop: 0}, nil
					}
					if n, convErr := strconv.Atoi(slopText); convErr == nil {
						if n < 0 {
							return nil, &ParseError{Pos: pos + open, Message: "proximity slop must be non-negative"}
						}
						return proximity{A: a, B: b, Slop: n}, nil
					}
				}
			}
		}
	}
	if strings.HasSuffix(word, ":*") {
		stem := word[:len(word)-2]
		if stem != "" {
			return prefixToken{Stem: stem}, nil
		}
	}
	return bareToken{Text: word}, nil
}

// tokState is the cursor consumed by the Pratt parser.
type tokState struct {
	toks   []chunkTok
	idx    int
	endPos int
}

func (s *tokState) peek() *chunkTok {
	if s.idx >= len(s.toks) {
		return nil
	}
	return &s.toks[s.idx]
}

func (s *tokState) advance() *chunkTok {
	t := s.peek()
	if t != nil {
		s.idx++
	}
	return t
}

// parseOr consumes `OR_EXPR ::= AND_EXPR ('|' AND_EXPR)*` with `|` having
// the lowest precedence per PostgreSQL ts_query.
func parseOr(s *tokState) (queryNode, error) {
	left, err := parseAnd(s)
	if err != nil {
		return nil, err
	}
	for {
		t := s.peek()
		if t == nil || t.kind != tokOr {
			break
		}
		opPos := t.pos
		s.advance()
		right, err := parseAnd(s)
		if err != nil {
			return nil, err
		}
		if right == nil {
			return nil, &ParseError{Pos: opPos + 1, Message: "operator '|' missing right operand"}
		}
		if left == nil {
			return nil, &ParseError{Pos: opPos, Message: "operator '|' missing left operand"}
		}
		if d, ok := left.(disjunction); ok {
			d.Children = append(d.Children, right)
			left = d
		} else {
			left = disjunction{Children: []queryNode{left, right}}
		}
	}
	return left, nil
}

// parseAnd consumes `AND_EXPR ::= NOT_EXPR ('&' NOT_EXPR)*` with `&`
// binding tighter than `|`.
func parseAnd(s *tokState) (queryNode, error) {
	left, err := parseNot(s)
	if err != nil {
		return nil, err
	}
	for {
		t := s.peek()
		if t == nil || t.kind != tokAnd {
			break
		}
		opPos := t.pos
		s.advance()
		right, err := parseNot(s)
		if err != nil {
			return nil, err
		}
		if right == nil {
			return nil, &ParseError{Pos: opPos + 1, Message: "operator '&' missing right operand"}
		}
		if left == nil {
			return nil, &ParseError{Pos: opPos, Message: "operator '&' missing left operand"}
		}
		if c, ok := left.(conjunction); ok {
			c.Children = append(c.Children, right)
			left = c
		} else {
			left = conjunction{Children: []queryNode{left, right}}
		}
	}
	return left, nil
}

// parseNot consumes `NOT_EXPR ::= '!' NOT_EXPR | ATOM`. The recursion
// makes `!!a` legal (double negation) without requiring extra grammar.
func parseNot(s *tokState) (queryNode, error) {
	t := s.peek()
	if t != nil && t.kind == tokNot {
		opPos := t.pos
		s.advance()
		child, err := parseNot(s)
		if err != nil {
			return nil, err
		}
		if child == nil {
			return nil, &ParseError{Pos: opPos + 1, Message: "operator '!' missing right operand"}
		}
		return mustNot{Child: child}, nil
	}
	return parseAtom(s)
}

// parseAtom returns nil (without error) when the next token is not an
// atom — the binary-op layers convert that into a "missing operand"
// ParseError positioned at the trailing operator.
func parseAtom(s *tokState) (queryNode, error) {
	t := s.peek()
	if t == nil || t.kind != tokAtom {
		return nil, nil
	}
	s.advance()
	return t.atom, nil
}

// lower converts an AST node into a Bleve query per research § R4
// mapping table. Analyzed-text productions emit a disjunction across
// `text_en` and `text_cjk` so a single source string is matchable in
// either language family. Exact / prefix productions target `text_raw`
// (keyword-analyzed, lowercased) so single-character and punctuation
// matches survive.
func lower(node queryNode) query.Query {
	switch n := node.(type) {
	case bareToken:
		return matchTextEnOrCjk(n.Text)
	case accentToken:
		return matchTextEnOrCjk(n.Text)
	case phraseToken:
		return phraseTextEnOrCjk(n.Text)
	case proximity:
		if n.Slop == 0 {
			return phraseTextEnOrCjk(n.A + " " + n.B)
		}
		// Bleve has no phrase-slop primitive. Approximate `<N>` as a
		// conjunction requiring both words to appear in the document
		// (loosest reasonable interpretation; covers US2 scenario 4).
		return bleve.NewConjunctionQuery(matchTextEnOrCjk(n.A), matchTextEnOrCjk(n.B))
	case exactToken:
		t := bleve.NewTermQuery(foldRaw(n.Text))
		t.SetField(fieldTextRaw)
		return t
	case prefixToken:
		p := bleve.NewPrefixQuery(foldRaw(n.Stem))
		p.SetField(fieldTextRaw)
		return p
	case conjunction:
		sub := make([]query.Query, 0, len(n.Children))
		for _, c := range n.Children {
			if q := lower(c); q != nil {
				sub = append(sub, q)
			}
		}
		return bleve.NewConjunctionQuery(sub...)
	case disjunction:
		sub := make([]query.Query, 0, len(n.Children))
		for _, c := range n.Children {
			if q := lower(c); q != nil {
				sub = append(sub, q)
			}
		}
		return bleve.NewDisjunctionQuery(sub...)
	case mustNot:
		bq := bleve.NewBooleanQuery()
		// MustNot alone over-matches; pair with a match-all so the boolean
		// query has a positive base set to subtract from.
		bq.AddMust(bleve.NewMatchAllQuery())
		if inner := lower(n.Child); inner != nil {
			bq.AddMustNot(inner)
		}
		return bq
	}
	return nil
}

// matchTextEnOrCjk emits the dual-field disjunction every analyzed-token
// query needs: the text matches if either text_en (Latin-stemmed) or
// text_cjk (CJK-bigram) yields a hit.
func matchTextEnOrCjk(text string) query.Query {
	en := bleve.NewMatchQuery(text)
	en.SetField(fieldTextEN)
	cjk := bleve.NewMatchQuery(text)
	cjk.SetField(fieldTextCJK)
	return bleve.NewDisjunctionQuery(en, cjk)
}

// phraseTextEnOrCjk emits the dual-field disjunction for an exact phrase.
// Bleve's MatchPhraseQuery requires adjacency; slop-bounded variants are
// handled by the proximity branch in lower().
func phraseTextEnOrCjk(text string) query.Query {
	en := bleve.NewMatchPhraseQuery(text)
	en.SetField(fieldTextEN)
	cjk := bleve.NewMatchPhraseQuery(text)
	cjk.SetField(fieldTextCJK)
	return bleve.NewDisjunctionQuery(en, cjk)
}
