package slug

import (
	"testing"
)

func TestGenerate_Length(t *testing.T) {
	for _, length := range []int{2, 3, 4, 5} {
		s := Generate(length)
		if len(s) != length {
			t.Errorf("Generate(%d) = %q (len %d), want len %d", length, s, len(s), length)
		}
	}
}

func TestGenerate_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		s := Generate(3)
		if seen[s] {
			// Collisions can happen but should be very rare in 1000 samples
			// with 238k possibilities. Just skip.
			continue
		}
		seen[s] = true
	}
	if len(seen) < 950 {
		t.Errorf("too many collisions: only %d unique slugs out of 1000", len(seen))
	}
}

func TestGenerate_ValidChars(t *testing.T) {
	for range 100 {
		s := Generate(3)
		for _, c := range s {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
				t.Errorf("Generate(3) = %q, contains invalid char %c", s, c)
			}
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		slug    string
		wantErr bool
	}{
		{"abc", false},
		{"my-link", false},
		{"test_123", false},
		{"ABC", false},
		{"", true},
		{"admin", true},
		{"shorten", true},
		{"a b", true},
		{"a/b", true},
	}

	for _, tt := range tests {
		err := Validate(tt.slug)
		if (err != nil) != tt.wantErr {
			t.Errorf("Validate(%q) error = %v, wantErr %v", tt.slug, err, tt.wantErr)
		}
	}
}
