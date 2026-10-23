package core

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var ErrAmbiguous = errors.New("ambiguous session")

// Short uses UUIDv7's random tail; other keys keep their first eight characters.
func Short(key string) string {
	if id, err := uuid.Parse(key); len(key) == 36 && err == nil && id.Version() == 7 {
		return key[len(key)-12:]
	}
	if len(key) > 8 {
		return key[:8]
	}
	return key
}

// SessionMatches accepts a full id, its displayed shorthand, or a prefix of at least six characters.
func SessionMatches(id, ref string) bool {
	return id != "" && (id == ref || len(ref) >= 6 && (strings.HasPrefix(id, ref) || Short(id) == ref))
}

// SessionRef resolves one distinct full id, preferring an exact id over shorthand matches.
func SessionRef(ref string, ids []string) (string, error) {
	var hit string
	for _, id := range ids {
		if id != "" && id == ref {
			return id, nil
		}
	}
	for _, id := range ids {
		if !SessionMatches(id, ref) || id == hit {
			continue
		}
		if hit != "" {
			return "", fmt.Errorf("%w %q: %s and %s; use a full session id", ErrAmbiguous, ref, hit, id)
		}
		hit = id
	}
	if hit == "" {
		return "", fmt.Errorf("session %q: %w", ref, ErrNotFound)
	}
	return hit, nil
}
