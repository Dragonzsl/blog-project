package slug

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const MaxRunes = 120

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
