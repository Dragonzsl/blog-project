package id

import (
	"bytes"
	"testing"
	"time"
)

func TestNewPublicIDContainsOrderedTimestampAndRandomness(t *testing.T) {
	first, err := NewPublicID(time.UnixMilli(1000))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPublicID(time.UnixMilli(2000))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 16 || len(second) != 16 {
		t.Fatalf("identifier lengths = %d, %d", len(first), len(second))
	}
	if bytes.Compare(first[:6], second[:6]) >= 0 {
		t.Fatal("timestamp prefix is not ordered")
	}
	if bytes.Equal(first[6:], second[6:]) {
		t.Fatal("random suffixes are equal")
	}
}
