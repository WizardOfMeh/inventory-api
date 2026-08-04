package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Cursor marks the last row of a page so the next query can resume from it.
//
// It is opaque on purpose: clients must treat it as a token to echo back, not
// as a structure to build themselves. That keeps the encoding free to change
// (add a tiebreaker column, switch to a compound key) without breaking anyone.
type Cursor struct {
	// Sort binds the cursor to the ordering it was produced under. Resuming a
	// ram_desc scan with a name_asc cursor would silently skip and duplicate
	// rows, so the mismatch is rejected instead.
	Sort SortKey `json:"s"`
	// Key is the sort column value of the last row, kept as a string so one
	// cursor type covers both integer and text orderings.
	Key string `json:"k"`
	// ID breaks ties: sort columns are not unique, and without a second
	// comparison column rows sharing a value would be lost between pages.
	ID int `json:"i"`
}

// Encode renders the cursor as a URL-safe token.
func (c Cursor) Encode() string {
	b, err := json.Marshal(c)
	if err != nil {
		// Cursor contains only strings and an int, so this cannot fail.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a token produced by Encode and verifies it belongs to
// the requested ordering.
func DecodeCursor(raw string, want SortKey) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, fmt.Errorf("cursor is not valid base64")
	}

	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return Cursor{}, fmt.Errorf("cursor is malformed")
	}
	if c.ID <= 0 || c.Key == "" {
		return Cursor{}, fmt.Errorf("cursor is incomplete")
	}
	if c.Sort != want {
		return Cursor{}, fmt.Errorf("cursor belongs to sort %q, not %q", c.Sort, want)
	}

	return c, nil
}
