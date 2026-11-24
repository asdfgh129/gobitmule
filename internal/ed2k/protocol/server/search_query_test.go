package server

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

func TestTokenizeSearchQuery(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		wantText []string
		wantOps  []byte
	}{
		{
			name:     "multiple terms default to AND",
			query:    "windows 11 office",
			wantText: []string{"windows", "11", "office"},
			wantOps:  []byte{searchBoolAnd, searchBoolAnd, searchBoolAnd},
		},
		{
			name:     "explicit OR joins only the next term",
			query:    "windows 11 OR windows 10",
			wantText: []string{"windows", "11", "windows", "10"},
			wantOps:  []byte{searchBoolAnd, searchBoolAnd, searchBoolOr, searchBoolAnd},
		},
		{
			name:     "leading OR is ignored",
			query:    "OR windows",
			wantText: []string{"windows"},
			wantOps:  []byte{searchBoolAnd},
		},
		{
			name:     "AND cancels pending OR",
			query:    "windows OR AND 11",
			wantText: []string{"windows", "11"},
			wantOps:  []byte{searchBoolAnd, searchBoolAnd},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := tokenizeSearchQuery(tt.query)
			texts := make([]string, 0, len(tokens))
			ops := make([]byte, 0, len(tokens))
			for _, token := range tokens {
				texts = append(texts, token.text)
				ops = append(ops, token.op)
			}
			if !reflect.DeepEqual(texts, tt.wantText) {
				t.Fatalf("tokenizeSearchQuery(%q) texts = %v, want %v", tt.query, texts, tt.wantText)
			}
			if !reflect.DeepEqual(ops, tt.wantOps) {
				t.Fatalf("tokenizeSearchQuery(%q) ops = %v, want %v", tt.query, ops, tt.wantOps)
			}
		})
	}
}

func TestSearchRequestPutEncodesBoolOperators(t *testing.T) {
	var got bytes.Buffer
	req := SearchRequest{Query: "windows 11 OR windows 10"}
	if err := req.Put(&got); err != nil {
		t.Fatalf("Put: %v", err)
	}

	stringTerm := func(value string) []byte {
		var buf bytes.Buffer
		buf.WriteByte(searchTypeString)
		_ = protocol.WriteUInt16(&buf, uint16(len(value)))
		buf.WriteString(value)
		return buf.Bytes()
	}
	want := bytes.Join([][]byte{
		stringTerm("windows"),
		{searchTypeBool, searchBoolAnd},
		stringTerm("11"),
		{searchTypeBool, searchBoolOr},
		stringTerm("windows"),
		{searchTypeBool, searchBoolAnd},
		stringTerm("10"),
	}, nil)

	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("Put() = % x, want % x", got.Bytes(), want)
	}
}
