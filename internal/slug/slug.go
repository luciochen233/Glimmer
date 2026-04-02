package slug

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
)

const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

var reservedSlugs = map[string]bool{
	"admin":   true,
	"shorten": true,
	"static":  true,
	"api":     true,
	"health":  true,
}

var validSlugRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func Generate(length int) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return string(b)
}

func Validate(slug string) error {
	if len(slug) == 0 {
		return fmt.Errorf("slug cannot be empty")
	}
	if len(slug) > 64 {
		return fmt.Errorf("slug too long (max 64 characters)")
	}
	if !validSlugRe.MatchString(slug) {
		return fmt.Errorf("slug can only contain letters, numbers, hyphens, and underscores")
	}
	if reservedSlugs[strings.ToLower(slug)] {
		return fmt.Errorf("slug %q is reserved", slug)
	}
	return nil
}
