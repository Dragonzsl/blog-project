package pagination

import "testing"

func TestNormalizeAndNewInfo(t *testing.T) {
	request := Normalize(Request{Page: 0, PerPage: 200}, 20, 50)
	if request.Page != 1 || request.PerPage != 50 {
		t.Fatalf("normalized request = %+v", request)
	}

	info := NewInfo(101, Request{Page: 2, PerPage: 20})
	if info.Page != 2 || info.PageCount != 6 || info.Total != 101 || !info.HasPrevious || !info.HasNext || info.Offset() != 20 {
		t.Fatalf("page info = %+v offset=%d", info, info.Offset())
	}

	last := NewInfo(101, Request{Page: 99, PerPage: 20})
	if last.Page != 6 || last.HasNext || !last.HasPrevious || last.Offset() != 100 {
		t.Fatalf("out-of-range page info = %+v offset=%d", last, last.Offset())
	}

	empty := NewInfo(0, Request{Page: 3, PerPage: 20})
	if empty.Page != 1 || empty.PageCount != 0 || empty.HasPrevious || empty.HasNext || empty.Offset() != 0 {
		t.Fatalf("empty page info = %+v offset=%d", empty, empty.Offset())
	}
}
