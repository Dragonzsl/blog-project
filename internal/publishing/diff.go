package publishing

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrDiffTooLarge = errors.New("revision diff is too large")

const maxDiffBodyBytes = 1 << 20

type RevisionFieldChange struct {
	Field  string
	Before string
	After  string
}

type RevisionLine struct {
	Kind string
	Text string
}

type RevisionComparison struct {
	From      Revision
	To        Revision
	Changes   []RevisionFieldChange
	BodyLines []RevisionLine
}

func (s *Service) CompareRevisions(ctx context.Context, kind string, contentID, fromID, toID int64) (RevisionComparison, error) {
	from, err := s.Revision(ctx, kind, contentID, fromID)
	if err != nil {
		return RevisionComparison{}, err
	}
	to, err := s.Revision(ctx, kind, contentID, toID)
	if err != nil {
		return RevisionComparison{}, err
	}
	if len(from.BodyMarkdown) > maxDiffBodyBytes || len(to.BodyMarkdown) > maxDiffBodyBytes {
		return RevisionComparison{}, ErrDiffTooLarge
	}
	return RevisionComparison{From: from, To: to, Changes: revisionChanges(from, to), BodyLines: diffLines(from.BodyMarkdown, to.BodyMarkdown)}, nil
}

func revisionChanges(from, to Revision) []RevisionFieldChange {
	values := []struct {
		name, before, after string
	}{
		{"标题", from.Title, to.Title},
		{"固定链接", from.Slug, to.Slug},
		{"摘要", from.Excerpt, to.Excerpt},
		{"SEO 标题", from.SEOTitle, to.SEOTitle},
		{"SEO 描述", from.SEODescription, to.SEODescription},
		{"封面媒体公共 ID", fmt.Sprintf("%x", from.CoverMediaPublicID), fmt.Sprintf("%x", to.CoverMediaPublicID)},
		{"分类公共 ID", fmt.Sprintf("%x", from.CategoryPublicID), fmt.Sprintf("%x", to.CategoryPublicID)},
		{"标签快照", from.TagPublicIDsJSON, to.TagPublicIDsJSON},
	}
	result := make([]RevisionFieldChange, 0, len(values))
	for _, value := range values {
		if value.before != value.after {
			result = append(result, RevisionFieldChange{Field: value.name, Before: value.before, After: value.after})
		}
	}
	return result
}

// diffLines deliberately uses a bounded greedy look-ahead. It keeps the
// compare endpoint useful for long Markdown without allocating an unbounded
// LCS matrix, while still grouping nearby additions and removals clearly.
func diffLines(before, after string) []RevisionLine {
	left, right := strings.Split(before, "\n"), strings.Split(after, "\n")
	result := make([]RevisionLine, 0, len(left)+len(right))
	for i, j := 0, 0; i < len(left) || j < len(right); {
		if i < len(left) && j < len(right) && left[i] == right[j] {
			result = append(result, RevisionLine{Kind: "same", Text: left[i]})
			i++
			j++
			continue
		}
		if i >= len(left) {
			result = append(result, RevisionLine{Kind: "added", Text: right[j]})
			j++
			continue
		}
		if j >= len(right) {
			result = append(result, RevisionLine{Kind: "removed", Text: left[i]})
			i++
			continue
		}
		matchLeft, matchRight := -1, -1
		for look := 1; look <= 24 && (i+look < len(left) || j+look < len(right)); look++ {
			if matchLeft < 0 && i+look < len(left) && left[i+look] == right[j] {
				matchLeft = i + look
			}
			if matchRight < 0 && j+look < len(right) && left[i] == right[j+look] {
				matchRight = j + look
			}
		}
		if matchLeft >= 0 && (matchRight < 0 || matchLeft-i <= matchRight-j) {
			for i < matchLeft {
				result = append(result, RevisionLine{Kind: "removed", Text: left[i]})
				i++
			}
			continue
		}
		if matchRight >= 0 {
			for j < matchRight {
				result = append(result, RevisionLine{Kind: "added", Text: right[j]})
				j++
			}
			continue
		}
		result = append(result, RevisionLine{Kind: "removed", Text: left[i]}, RevisionLine{Kind: "added", Text: right[j]})
		i++
		j++
	}
	return result
}
