package SSTorytime

import (
	"context"
	"fmt"

	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/blevesearch/bleve/v2/search/searcher"
	index "github.com/blevesearch/bleve_index_api"
)

// proximityQuery is a Bleve Query that matches an ordered chain of operands
// with a fixed number of "any-token" slots between each pair, mirroring the
// PostgreSQL ts_query operators `<->` and `<N>`.
//
// Each operand is analyzed at search time using the field's configured
// analyzer (so stemming and accent-folding apply). The resulting tokens are
// concatenated into a multi-phrase, with `slops[i]` empty placeholder slots
// inserted between operand i and operand i+1. An empty placeholder in
// Bleve's phrase searcher matches any term at that position, giving exact
// positional control without needing the slop primitive that
// MatchPhraseQuery doesn't expose.
//
// Slops semantics:
//
//	`a<->b`  → slops=[0]   adjacent (no placeholder)
//	`a<2>b`  → slops=[1]   one placeholder slot between
//	`a<N>b`  → slops=[N-1] N-1 placeholders
type proximityQuery struct {
	operands []string
	slops    []int
	field    string
	boost    *query.Boost
}

func newProximityQuery(operands []string, slops []int, field string) *proximityQuery {
	return &proximityQuery{
		operands: operands,
		slops:    slops,
		field:    field,
	}
}

func (q *proximityQuery) SetBoost(b float64) {
	v := query.Boost(b)
	q.boost = &v
}

func (q *proximityQuery) Boost() float64 {
	if q.boost == nil {
		return 1.0
	}
	return q.boost.Value()
}

func (q *proximityQuery) Searcher(
	ctx context.Context,
	i index.IndexReader,
	m mapping.IndexMapping,
	options search.SearcherOptions,
) (search.Searcher, error) {
	if len(q.operands) == 0 {
		return searcher.NewMatchNoneSearcher(i)
	}
	if len(q.slops) != len(q.operands)-1 {
		return nil, fmt.Errorf(
			"proximityQuery: slops length %d != operands length-1 %d",
			len(q.slops), len(q.operands)-1,
		)
	}

	analyzerName := m.AnalyzerNameForPath(q.field)
	analyzer := m.AnalyzerNamed(analyzerName)
	if analyzer == nil {
		return nil, fmt.Errorf(
			"proximityQuery: no analyzer registered for field %q", q.field,
		)
	}

	// Build the multi-phrase: operand tokens, with q.slops[idx-1] empty
	// placeholder slots between operand idx-1 and operand idx.
	var phrase [][]string
	for idx, op := range q.operands {
		if idx > 0 {
			for k := 0; k < q.slops[idx-1]; k++ {
				phrase = append(phrase, []string{""})
			}
		}
		tokens := analyzer.Analyze([]byte(op))
		if len(tokens) == 0 {
			// Operand entirely filtered out (e.g. all stop-words). Treat
			// as a placeholder so the chain doesn't collapse into nothing.
			phrase = append(phrase, []string{""})
			continue
		}
		for _, t := range tokens {
			phrase = append(phrase, []string{string(t.Term)})
		}
	}
	if len(phrase) == 0 {
		return searcher.NewMatchNoneSearcher(i)
	}

	return searcher.NewMultiPhraseSearcher(
		ctx, i, phrase, 0 /*fuzziness*/, false /*autoFuzzy*/, q.field, q.Boost(), options,
	)
}
