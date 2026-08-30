package exa

import "testing"

func TestDetectAcceptsOnlyExactExaObject(t *testing.T) {
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{name: "exact", input: `{"tool":"exa.search","arguments":{"query":"go","num_results":8}}`, ok: true},
		{name: "default count", input: `{"tool":"exa.search","arguments":{"query":"go"}}`, ok: true},
		{name: "prefix", input: `prefix {"tool":"exa.search","arguments":{"query":"go"}}`},
		{name: "top extra", input: `{"tool":"exa.search","arguments":{"query":"go"},"extra":1}`},
		{name: "argument extra", input: `{"tool":"exa.search","arguments":{"query":"go","extra":1}}`},
		{name: "too many", input: `{"tool":"exa.search","arguments":{"query":"go","num_results":101}}`},
		{name: "zero", input: `{"tool":"exa.search","arguments":{"query":"go","num_results":0}}`},
		{name: "wrong tool", input: `{"tool":"web.search","arguments":{"query":"go"}}`},
		{name: "empty query", input: `{"tool":"exa.search","arguments":{"query":"  "}}`},
		{name: "trailing document", input: `{"tool":"exa.search","arguments":{"query":"go"}} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, ok := Detect(test.input)
			if ok != test.ok {
				t.Fatalf("Detect(%q) = %v, want %v", test.input, ok, test.ok)
			}
		})
	}
	got, ok := Detect(`{"tool":"exa.search","arguments":{"query":" go "}}`)
	if !ok || got.Query != "go" || got.NumResults != 10 {
		t.Fatalf("request = %#v, ok = %v", got, ok)
	}
}
