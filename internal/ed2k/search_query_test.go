package goed2k

import (
	"reflect"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

func TestParseSearchQueryGroups(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  [][]string
	}{
		{name: "multiple terms default to AND", query: "windows 11 office", want: [][]string{{"windows", "11", "office"}}},
		{name: "explicit OR splits groups", query: "windows 11 OR windows 10", want: [][]string{{"windows", "11"}, {"windows", "10"}}},
		{name: "explicit AND is a no-op", query: "windows AND 11", want: [][]string{{"windows", "11"}}},
		{name: "punctuation acts as separator", query: "windows,11;office", want: [][]string{{"windows", "11", "office"}}},
		{name: "operator without terms is empty", query: " OR ", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSearchQuery(tt.query).Groups
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseSearchQuery(%q).Groups = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestSearchQueryMatches(t *testing.T) {
	query := parseSearchQuery("windows 11 OR linux iso")
	tests := []struct {
		name     string
		fileName string
		hash     protocol.Hash
		want     bool
	}{
		{name: "first OR group", fileName: "Windows 11 Pro.iso", want: true},
		{name: "second OR group", fileName: "ubuntu linux iso", want: true},
		{name: "partial group", fileName: "Windows 10 Pro.iso", want: false},
		{name: "shared term only", fileName: "windows server iso", want: false},
		{name: "unrelated", fileName: "linux mint", want: false},
		{
			name:     "hash digits must not satisfy a keyword",
			fileName: "Windows 10 Pro.iso",
			hash:     protocol.MustHashFromString("11111111111111111111111111111111"),
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SearchResult{FileName: tt.fileName, Hash: tt.hash}
			if got := query.Matches(result); got != tt.want {
				t.Fatalf("Matches(%q) = %v, want %v", tt.fileName, got, tt.want)
			}
		})
	}
}

func TestSearchQueryKeywordsPerGroup(t *testing.T) {
	query := parseSearchQuery("windows 11 OR linux iso")
	got := query.Keywords(4)
	want := []string{"windows", "linux"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Keywords(4) = %v, want %v", got, want)
	}
}

func TestMatchesSearchFiltersCombinations(t *testing.T) {
	baseResult := SearchResult{
		FileName:        "Windows 11 Pro.iso",
		FileSize:        500 * 1024 * 1024,
		Sources:         5,
		CompleteSources: 2,
		Extension:       ".iso",
		FileType:        "archive",
		Source:          SearchResultServer | SearchResultKAD,
	}
	tests := []struct {
		name   string
		params SearchParams
		mutate func(*SearchResult)
		want   bool
	}{
		{name: "default AND", params: SearchParams{Query: "windows 11"}, want: true},
		{name: "AND rejects partial match", params: SearchParams{Query: "windows office"}, want: false},
		{name: "explicit OR accepts one group", params: SearchParams{Query: "linux OR windows"}, want: true},
		{name: "file type hit", params: SearchParams{Query: "windows", FileTypes: []string{"archive"}}, want: true},
		{name: "file type miss", params: SearchParams{Query: "windows", FileTypes: []string{"video"}}, want: false},
		{name: "extension hit", params: SearchParams{Query: "windows", Extensions: []string{"iso"}}, want: true},
		{name: "extension miss", params: SearchParams{Query: "windows", Extensions: []string{"mp4"}}, want: false},
		{
			name:   "type OR extension",
			params: SearchParams{Query: "windows", FileTypes: []string{"video"}, Extensions: []string{"iso"}},
			want:   true,
		},
		{name: "source both", params: SearchParams{Query: "windows", Sources: []string{"both"}}, want: true},
		{name: "source server", params: SearchParams{Query: "windows", Sources: []string{"server"}}, want: true},
		{
			name:   "source KAD misses server-only result",
			params: SearchParams{Query: "windows", Sources: []string{"kad"}},
			mutate: func(r *SearchResult) { r.Source = SearchResultServer },
			want:   false,
		},
		{name: "min size", params: SearchParams{Query: "windows", MinSize: 600 * 1024 * 1024}, want: false},
		{name: "min sources", params: SearchParams{Query: "windows", MinSources: 6}, want: false},
		{name: "min complete sources", params: SearchParams{Query: "windows", MinCompleteSources: 3}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := baseResult
			if tt.mutate != nil {
				tt.mutate(&result)
			}
			if got := matchesSearchFilters(result, tt.params); got != tt.want {
				t.Fatalf("matchesSearchFilters() = %v, want %v", got, tt.want)
			}
		})
	}
}
