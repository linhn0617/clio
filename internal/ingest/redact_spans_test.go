package ingest

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

// Compare the memory-only rewrite to the pre-review scanner, frozen from
// b7517e5. In particular, both quote contexts and invalid spans must agree.
func TestBalancedJSONSpanMemoryRewriteEquivalent(t *testing.T) {
	rng := rand.New(rand.NewSource(1857))
	for i := 0; i < 20000; i++ {
		var text strings.Builder
		for n := rng.Intn(120); n > 0; n-- {
			text.WriteByte("[]{}\\\"a :0,\n"[rng.Intn(12)])
		}
		in := text.String()
		if i%2 == 0 {
			in += `{"nested":[{"token":"hunter2"}]}`
		}
		want, got := balancedJSONSpansReference(in), balancedJSONSpans(in)
		for pos, span := range want {
			if got[pos] != span {
				t.Fatalf("span mismatch at %d in %q: got %+v want %+v", pos, in, got[pos], span)
			}
		}
	}
}

// Original allocation-heavy scanner, retained only as a semantic reference.
func balancedJSONSpansReference(s string) []jsonSpan {
	spans := make([]jsonSpan, len(s))
	type frame struct {
		start, depth int
		shallow      []byte
	}
	var stacks [2][]frame
	parity, slashes := 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		// This byte is ordinary string content in the other quote context.
		other := &stacks[1-parity]
		if n := len(*other); n > 0 {
			(*other)[n-1].shallow = append((*other)[n-1].shallow, c)
		}
		stack := &stacks[parity]
		switch c {
		case '{', '[':
			*stack = append(*stack, frame{start: i, depth: 1, shallow: []byte{c}})
		case '}', ']':
			n := len(*stack)
			if n > 0 {
				f := (*stack)[n-1]
				if (s[f.start] == '{' && c != '}') || (s[f.start] == '[' && c != ']') {
					*stack = (*stack)[:0]
				} else {
					f.shallow = append(f.shallow, c)
					valid := json.Valid(f.shallow) && f.depth <= 10000 // encoding/json's nesting limit
					spans[f.start] = jsonSpan{i + 1, valid}
					*stack = (*stack)[:n-1]
					if n > 1 {
						parent := &(*stack)[n-2]
						if f.depth+1 > parent.depth {
							parent.depth = f.depth + 1
						}
						if valid {
							parent.shallow = append(parent.shallow, ' ', '0', ' ')
						} else {
							parent.shallow = append(parent.shallow, ' ', '!', ' ')
						}
					}
				}
			}
		default:
			if n := len(*stack); n > 0 {
				(*stack)[n-1].shallow = append((*stack)[n-1].shallow, c)
			}
		}
		if c == '"' && slashes%2 == 0 {
			parity = 1 - parity
		}
		if c == '\\' {
			slashes++
		} else {
			slashes = 0
		}
	}
	return spans
}
