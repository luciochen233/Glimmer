package slug

import (
	"crypto/rand"
	"fmt"
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
	"bin":     true,
	"uploads": true,
	"mcp":     true,
	"login":   true,
}

var validSlugRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Generate returns a random string of the given length drawn from the
// alphabet using a cryptographically secure source. It is safe to use for
// access tokens as well as public slugs. Uses rejection sampling so the
// 36-character alphabet maps onto bytes without modulo bias.
func Generate(length int) string {
	const maxUnbiased = 256 - (256 % len(alphabet)) // 252
	out := make([]byte, length)
	buf := make([]byte, length)
	filled := 0
	for filled < length {
		if _, err := rand.Read(buf); err != nil {
			// crypto/rand should never fail; if it does, panic rather than
			// silently emit a predictable token.
			panic("slug: crypto/rand failed: " + err.Error())
		}
		for _, b := range buf {
			if int(b) >= maxUnbiased {
				continue
			}
			out[filled] = alphabet[int(b)%len(alphabet)]
			filled++
			if filled == length {
				break
			}
		}
	}
	return string(out)
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
