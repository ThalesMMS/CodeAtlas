package store

import (
	"math"
	"sort"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lexical"
	"github.com/ThalesMMS/CodeAtlas/internal/storederive"
)

type lexicalIndex struct {
	postings    map[string]map[string]int
	docLen      map[string]int
	boostFields map[string]lexicalBoostFields
	docCount    int
	avgLen      float64
}

type lexicalBoostFields struct {
	name      string
	qualified string
}

func newLexicalIndex() *lexicalIndex {
	return &lexicalIndex{
		postings:    make(map[string]map[string]int),
		docLen:      make(map[string]int),
		boostFields: make(map[string]lexicalBoostFields),
	}
}

func (l *lexicalIndex) add(symbol domain.Symbol) {
	text := strings.Join([]string{
		symbol.Name, symbol.Name, symbol.Name, symbol.Name,
		symbol.QualifiedName, symbol.QualifiedName,
		symbol.Kind, symbol.Path, symbol.Signature, symbol.DocComment, symbol.Summary, symbol.Code,
	}, " ")
	tokens := lexical.Tokenize(text)
	l.docLen[symbol.ID] = len(tokens)
	l.boostFields[symbol.ID] = lexicalBoostFields{
		name:      strings.ToLower(symbol.Name),
		qualified: strings.ToLower(symbol.QualifiedName),
	}
	l.docCount++
	for _, token := range tokens {
		posting := l.postings[token]
		if posting == nil {
			posting = make(map[string]int)
			l.postings[token] = posting
		}
		posting[symbol.ID]++
	}
}

func (l *lexicalIndex) finalize() {
	if l.docCount == 0 {
		l.avgLen = 1
		return
	}
	total := 0
	for _, length := range l.docLen {
		total += length
	}
	l.avgLen = float64(total) / float64(l.docCount)
	if l.avgLen < 1 {
		l.avgLen = 1
	}
}

func (l *lexicalIndex) search(query string, limit int, symbols map[string]domain.Symbol) []domain.SearchHit {
	queryTokens := lexical.BuildQuery(query)
	if len(queryTokens) == 0 {
		return nil
	}

	const k1 = 1.5
	const b = 0.75
	scores := make(map[string]float64)
	for _, token := range queryTokens {
		posting := l.postings[token]
		if len(posting) == 0 {
			continue
		}
		idf := math.Log(1 + (float64(l.docCount-len(posting))+0.5)/(float64(len(posting))+0.5))
		for id, frequency := range posting {
			docLength := float64(l.docLen[id])
			tf := float64(frequency)
			denominator := tf + k1*(1-b+b*docLength/l.avgLen)
			scores[id] += idf * (tf * (k1 + 1) / denominator)
		}
	}

	queryLower := strings.ToLower(strings.TrimSpace(query))
	for id := range scores {
		fields := l.boostFields[id]
		if fields.name == queryLower {
			scores[id] += 8
		} else if strings.Contains(fields.name, queryLower) {
			scores[id] += 3
		}
		if strings.Contains(fields.qualified, queryLower) {
			scores[id] += 1.5
		}
	}

	type ranked struct {
		id    string
		score float64
	}
	ranking := make([]ranked, 0, len(scores))
	for id, score := range scores {
		ranking = append(ranking, ranked{id: id, score: score})
	}
	sort.Slice(ranking, func(i, j int) bool { return ranking[i].score > ranking[j].score })
	if len(ranking) > limit {
		ranking = ranking[:limit]
	}

	result := make([]domain.SearchHit, 0, len(ranking))
	for _, item := range ranking {
		symbol, ok := symbols[item.id]
		if !ok {
			continue
		}
		result = append(result, domain.SearchHit{
			Symbol: symbol, Snippet: storederive.Snippet(symbol, queryTokens), Score: item.score, Source: "bm25",
		})
	}
	return result
}
