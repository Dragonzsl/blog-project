package slug

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const MaxRunes = 120

// FromTitle creates a stable, URL-safe display slug from a content title.
// Letters and numbers are kept in their original writing system so titles in
// Chinese, Japanese, and other Unicode scripts remain readable in the URL.
func FromTitle(title, fallback string) (string, string, error) {
	title = norm.NFC.String(strings.TrimSpace(title))
	var builder strings.Builder
	previousHyphen := false
	for _, current := range title {
		if unicode.IsLetter(current) || unicode.IsNumber(current) {
			builder.WriteRune(unicode.ToLower(current))
			previousHyphen = false
			continue
		}
		if builder.Len() > 0 && !previousHyphen {
			builder.WriteByte('-')
			previousHyphen = true
		}
	}
	candidate := strings.Trim(builder.String(), "-")
	if candidate == "" {
		candidate = norm.NFC.String(strings.TrimSpace(fallback))
	}
	if utf8.RuneCountInString(candidate) > MaxRunes {
		candidate = strings.TrimRight(string([]rune(candidate)[:MaxRunes]), "-")
	}
	return Normalize(candidate)
}

// AppendSuffix adds a numeric collision suffix while keeping the slug within
// the same validation and length rules as Normalize.
func AppendSuffix(value string, suffix int) (string, string, error) {
	if suffix < 2 {
		return Normalize(value)
	}
	display, _, err := Normalize(value)
	if err != nil {
		return "", "", err
	}
	suffixText := fmt.Sprintf("-%d", suffix)
	limit := MaxRunes - utf8.RuneCountInString(suffixText)
	if limit < 1 {
		return "", "", fmt.Errorf("slug suffix exceeds maximum length")
	}
	base := []rune(display)
	if len(base) > limit {
		base = base[:limit]
	}
	return Normalize(strings.TrimRight(string(base), "-") + suffixText)
}

func Normalize(value string) (string, string, error) {
	display := norm.NFC.String(strings.TrimSpace(value))
	if display == "" || utf8.RuneCountInString(display) > MaxRunes {
		return "", "", fmt.Errorf("slug must contain 1-%d characters", MaxRunes)
	}
	previousHyphen := false
	for index, current := range []rune(display) {
		if current == '-' {
			if index == 0 || previousHyphen {
				return "", "", fmt.Errorf("slug cannot start with or repeat a hyphen")
			}
			previousHyphen = true
			continue
		}
		if !unicode.IsLetter(current) && !unicode.IsNumber(current) {
			return "", "", fmt.Errorf("slug can only contain letters, numbers, or single hyphens")
		}
		previousHyphen = false
	}
	if previousHyphen {
		return "", "", fmt.Errorf("slug cannot end with a hyphen")
	}
	return display, strings.ToLower(display), nil
}
