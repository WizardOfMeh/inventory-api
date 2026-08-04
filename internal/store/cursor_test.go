package store

import "testing"

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{Sort: SortRAMDesc, Key: "65024", ID: 499898}
	got, err := DecodeCursor(c.Encode(), SortRAMDesc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != c {
		t.Fatalf("got %+v want %+v", got, c)
	}
}

func TestCursorRejectsSortMismatch(t *testing.T) {
	c := Cursor{Sort: SortRAMDesc, Key: "65024", ID: 1}
	if _, err := DecodeCursor(c.Encode(), SortNameAsc); err == nil {
		t.Fatal("expected sort mismatch to be rejected")
	}
}

func TestCursorRejectsGarbage(t *testing.T) {
	for _, in := range []string{"!!!", "", "e30"} {
		if _, err := DecodeCursor(in, SortIDAsc); err == nil {
			t.Fatalf("expected %q to be rejected", in)
		}
	}
}

func TestParseSortKey(t *testing.T) {
	if k, ok := ParseSortKey(""); !ok || k != SortIDAsc {
		t.Fatal("empty sort should default to id_asc")
	}
	if _, ok := ParseSortKey("drop table"); ok {
		t.Fatal("unknown sort must be rejected")
	}
}
