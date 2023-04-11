// https://artem.krylysov.com/blog/2020/07/28/lets-build-a-full-text-search-engine/
package main

import (
	"strings"
	"unicode"

	snowballeng "github.com/kljensen/snowball/english"
)

type index map[string][]int

func (idx index) add(titles []string) {
	for id, title := range titles {
		for _, token := range analyze(title) {
			if contains(idx[token], id) {
				continue
			}
			idx[token] = append(idx[token], id)
		}
	}
}

func analyze(text string) []string {
	tokens := tokenize(text)
	tokens = toLower(tokens)
	tokens = removeCommonWords(tokens)
	tokens = stem(tokens)
	return tokens
}

func tokenize(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func toLower(tokens []string) []string {
	r := make([]string, len(tokens))
	for i, token := range tokens {
		r[i] = strings.ToLower(token)
	}
	return r
}

var stopWords = map[string]struct{}{
	"a":    {},
	"and":  {},
	"be":   {},
	"have": {},
	"i":    {},
	"in":   {},
	"of":   {},
	"that": {},
	"the":  {},
	"to":   {},
}

func removeCommonWords(tokens []string) []string {
	r := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, ok := stopWords[token]; !ok {
			r = append(r, token)
		}
	}
	return r
}

func stem(tokens []string) []string {
	r := make([]string, len(tokens))
	for i, token := range tokens {
		r[i] = snowballeng.Stem(token, false)
	}
	return r
}

func contains(slice []int, val int) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func intersection(a, b []int) []int {
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}

	r := make([]int, 0, maxLen)
	var i, j int
	for i < len(a) && j < len(b) {
		if a[i] < b[j] {
			i++
		} else if a[i] > b[j] {
			j++
		} else {
			r = append(r, a[i])
			i++
			j++
		}
	}
	return r
}

func (idx index) search(text string) []int {
	var r []int
	for _, token := range analyze(text) {
		if ids, ok := idx[token]; ok {
			if r == nil {
				r = ids
			} else {
				r = intersection(r, ids)
			}
		} else {
			return nil
		}
	}
	return r
}
