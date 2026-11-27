package goed2k

// #11: search result quality / spam rating, modelled on eMule's
// CSearchList::DoSpamRating and CSearchFile::UpdateFileRatingCommentAvail.
//
// eMule lets the user hide "fake" results: files whose name is padded with
// unrelated keywords, tiny files claiming to be movies, and executables
// disguised with a double extension. This file implements the same heuristics
// as a filter that the session applies to server, UDP and KAD results.

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SpamFilter rates search results. The zero value is not usable; use
// NewSpamFilter.
type SpamFilter struct {
	mu        sync.RWMutex
	badWords  []string
	minRating int
	enabled   bool
}

// NewSpamFilter returns a filter with built-in heuristics enabled.
func NewSpamFilter() *SpamFilter {
	return &SpamFilter{minRating: 2, enabled: true}
}

// SetEnabled turns filtering on/off (rating is still computed when off).
func (f *SpamFilter) SetEnabled(enabled bool) {
	f.mu.Lock()
	f.enabled = enabled
	f.mu.Unlock()
}

// Enabled reports whether spam results are dropped.
func (f *SpamFilter) Enabled() bool {
	if f == nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.enabled
}

// SetMinRating sets the lowest rating considered acceptable (default 2 of 5).
func (f *SpamFilter) SetMinRating(rating int) {
	if rating < 0 {
		rating = 0
	}
	if rating > 5 {
		rating = 5
	}
	f.mu.Lock()
	f.minRating = rating
	f.mu.Unlock()
}

// MinRating reports the configured floor.
func (f *SpamFilter) MinRating() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.minRating
}

// AddWord adds a spam keyword; matching is case-insensitive.
func (f *SpamFilter) AddWord(word string) {
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return
	}
	f.mu.Lock()
	f.badWords = append(f.badWords, word)
	f.mu.Unlock()
}

// WordCount reports how many custom keywords are loaded.
func (f *SpamFilter) WordCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.badWords)
}

// LoadWords loads one keyword per line; '#' starts a comment. The built-in
// heuristics stay active alongside the custom words.
func (f *SpamFilter) LoadWords(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var words []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		words = append(words, strings.ToLower(line))
	}
	if err := scanner.Err(); err != nil {
		return len(words), err
	}
	f.mu.Lock()
	f.badWords = append(f.badWords, words...)
	f.mu.Unlock()
	return len(words), nil
}

// SearchRating is the quality verdict for one result. Score runs 0..5, where 5
// is a well formed, widely available file and 0 is certainly fake.
type SearchRating struct {
	Score   int
	Spam    bool
	Reasons []string
}

// Rate scores a result name/size/source-count triple.
func (f *SpamFilter) Rate(name string, size int64, completeSources int) SearchRating {
	rating := SearchRating{Score: 3}
	lower := strings.ToLower(name)
	base := strings.ToLower(filepath.Base(name))
	ext := strings.TrimPrefix(filepath.Ext(base), ".")

	// A disguised executable is the strongest spam signal: "movie.avi.exe".
	if isExecutableExtension(ext) {
		if inner := strings.TrimSuffix(base, "."+ext); strings.Contains(inner, ".") {
			innerExt := strings.TrimPrefix(filepath.Ext(inner), ".")
			if isMediaOrDocExtension(innerExt) {
				rating.Score -= 3
				rating.Reasons = append(rating.Reasons, "double extension")
			}
		}
		rating.Score--
		rating.Reasons = append(rating.Reasons, "executable")
	}

	// Media or archive claiming an implausible size.
	if size > 0 && size < 10*1024 && (isMediaOrDocExtension(ext) || isArchiveExtension(ext)) {
		rating.Score -= 2
		rating.Reasons = append(rating.Reasons, "implausibly small")
	}

	// Custom keyword list.
	f.mu.RLock()
	words := f.badWords
	f.mu.RUnlock()
	hits := 0
	for _, word := range words {
		if strings.Contains(lower, word) {
			hits++
			if hits >= 3 {
				break
			}
		}
	}
	if hits > 0 {
		rating.Score -= hits
		rating.Reasons = append(rating.Reasons, "spam keywords")
	}

	// Keyword stuffing: eMule flags names that repeat the same token many
	// times to match every query.
	if repeatedTokenRatio(base) > 0.6 && len(strings.Fields(base)) >= 6 {
		rating.Score--
		rating.Reasons = append(rating.Reasons, "keyword stuffing")
	}

	// Well seeded files gain a star; no source information leaves the base.
	if completeSources >= 5 {
		rating.Score++
	}

	if rating.Score < 0 {
		rating.Score = 0
	}
	if rating.Score > 5 {
		rating.Score = 5
	}
	rating.Spam = rating.Score < f.MinRating() || len(rating.Reasons) > 0 && rating.Score <= 1
	return rating
}

// applySearchRating annotates a result and reports whether it should be kept.
func (s *Session) applySearchRating(result *SearchResult) bool {
	if s.spamFilter == nil {
		return true
	}
	rating := s.spamFilter.Rate(result.FileName, result.FileSize, result.CompleteSources)
	result.Rating = rating.Score
	result.Spam = rating.Spam
	if s.spamFilter.Enabled() && rating.Spam {
		return false
	}
	return true
}

// SearchFilter returns the session search filter (never nil).
func (s *Session) SearchFilter() *SpamFilter {
	if s.spamFilter == nil {
		s.spamFilter = NewSpamFilter()
	}
	return s.spamFilter
}

// SetSearchFilterRating changes the minimum acceptable rating.
func (s *Session) SetSearchFilterRating(rating int) {
	s.SearchFilter().SetMinRating(rating)
}

// LoadSearchFilterWords loads spam keywords from a file.
func (s *Session) LoadSearchFilterWords(path string) (int, error) {
	return s.SearchFilter().LoadWords(path)
}

func isExecutableExtension(ext string) bool {
	switch ext {
	case "exe", "scr", "bat", "cmd", "com", "pif", "vbs", "js", "jse", "wsf", "msi":
		return true
	}
	return false
}

func isMediaOrDocExtension(ext string) bool {
	switch ext {
	case "avi", "mkv", "mp4", "mpg", "mpeg", "wmv", "mov", "flv", "rmvb", "ts",
		"mp3", "flac", "wav", "ape", "aac", "ogg",
		"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "epub", "iso":
		return true
	}
	return false
}

func isArchiveExtension(ext string) bool {
	switch ext {
	case "zip", "rar", "7z", "tar", "gz", "bz2":
		return true
	}
	return false
}

// repeatedTokenRatio returns how much of the name is made of its most frequent
// token, ignoring very short separators.
func repeatedTokenRatio(lowerName string) float64 {
	fields := strings.FieldsFunc(lowerName, func(r rune) bool {
		return r == ' ' || r == '.' || r == '_' || r == '-' || r == '[' || r == ']' ||
			r == '(' || r == ')' || r == '+'
	})
	if len(fields) == 0 {
		return 0
	}
	counts := make(map[string]int, len(fields))
	total := 0
	for _, field := range fields {
		if len(field) < 2 {
			continue
		}
		counts[field]++
		total++
	}
	if total == 0 {
		return 0
	}
	max := 0
	for _, n := range counts {
		if n > max {
			max = n
		}
	}
	return float64(max) / float64(total)
}
