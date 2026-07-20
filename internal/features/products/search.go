package products

import (
	"strconv"
	"strings"
)

// Forgiving product search.
//
// The old rule was one substring match against the whole query, which punished
// the cashier for word order: "Bigo Trivago Yellow Flip Flop Size 4" was found
// by "yellow" and by "size 4", but NOT by "yellow flip flop size 4", because
// those words are not contiguous in that order. Under pressure at the till that
// is the difference between finding an item and giving up.
//
// The rule is now: split the query into words, and every word must match the
// name somehow. Order stops mattering, and each word gets three chances:
//
//  1. plain substring        — "yellow" in "... Yellow Flip Flop ..."
//  2. squashed substring     — "flipflop" in squash("Flip Flop") = "flipflop"
//  3. trigram similarity     — "yelow" ≈ "yellow"  (fuzzy pass only)
//
// Rule 3 runs only as a rescue pass, after a strict search found nothing. Fuzzy
// matching applied eagerly makes good searches worse: "philips" matches 7
// products exactly but 11 fuzzily, so the four extra rows would be pure noise
// on a search that already worked.

// searchTokens splits a raw query into the words that must all match. Returns
// nil for a blank query, which callers treat as "no search filter".
func searchTokens(raw string) []string {
	return strings.Fields(strings.ToLower(strings.TrimSpace(raw)))
}

// squashChars are the separators dropped when comparing the "squashed" forms of
// a name and a token, so "flipflop" finds "Flip Flop" and "a-4" finds "A4".
// This list is duplicated in the SQL below and in migration 0044's functional
// index — all three must agree or the index stops being used.
const squashChars = " -_/."

// squashedName is the Go mirror of the SQL translate() call. It exists so tests
// can assert the two implementations agree.
func squashedName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if !strings.ContainsRune(squashChars, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fuzzyThreshold is the minimum pg_trgm word_similarity for a token to count as
// a typo of something in the name.
//
// Measured against the real 615-product catalog, real typos and coincidences
// separate cleanly, so the threshold sits in the gap between them:
//
//	yelow     → "... Yellow Flip Flop ..."   0.625   want
//	philps    → "Philips AceBright 15W"      0.571   want
//	bok       → "Atlas CR Book 200 Pgs ..."  0.500   want
//	--------------------------------------- 0.45  ← threshold
//	asdf      → "Best Asia LED Headlight"    0.400   junk
//	blueberry → "Atlas Chooty 35 Blue Pen"   0.400   junk
//
// 0.4 was tried first and let both junk rows through: float4 rounding means a
// displayed 0.400 is not reliably < 0.4, so the boundary must not sit ON a
// cluster. This only runs after a strict search found nothing, so the downside
// of being slightly strict is an empty list the user retypes.
const fuzzyThreshold = 0.45

// searchClause is the WHERE fragment implementing the rules above.
//
// Placeholders, which the caller must bind in this order:
//
//	$tokens : text[]  — the query words; NULL/empty means "no search filter"
//	$raw    : text    — the untouched query, so a scanned barcode still matches
//	$fuzzy  : bool    — enable rule 3 (the rescue pass)
//
// Reads as: "no token fails to match", which is how ANDing a variable number of
// terms is expressed without building SQL by hand.
var searchClause = `
	($1::text[] IS NULL OR p.barcode = $2 OR NOT EXISTS (
		SELECT 1 FROM unnest($1::text[]) AS tok
		WHERE NOT (
			lower(p.name) LIKE '%' || tok || '%'
			OR translate(lower(p.name), ' -_/.', '') LIKE '%' || translate(tok, ' -_/.', '') || '%'
			OR ($3 AND word_similarity(tok, lower(p.name)) > ` + fuzzyThresholdSQL + `)
		)
	))`

// fuzzyThresholdSQL is fuzzyThreshold rendered for SQL, derived rather than
// written twice so the two can never drift apart.
var fuzzyThresholdSQL = strconv.FormatFloat(fuzzyThreshold, 'f', -1, 64)
