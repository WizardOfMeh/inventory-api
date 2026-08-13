package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Println("DATABASE_URL not set, skipping store integration tests")
		os.Exit(0)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ping db: %v\n", err)
		os.Exit(1)
	}
	testDB = db

	if err := applySchema(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "apply schema: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	_ = db.Close()
	os.Exit(code)
}

// applySchema runs the Up half of the initial migration. Reading the file
// keeps the test schema and the real one from drifting apart; goose is not
// pulled in as a dependency just for this.
func applySchema(ctx context.Context, db *sql.DB) error {
	raw, err := os.ReadFile("../../migrations/0001_init.sql")
	if err != nil {
		return err
	}
	body := string(raw)
	if i := strings.Index(body, "-- +goose Down"); i >= 0 {
		body = body[:i]
	}
	body = strings.ReplaceAll(body, "-- +goose Up", "")

	if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS vms, nodes CASCADE`); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, body)
	return err
}

// seed inserts one node and n VMs. Every ram value is repeated across several
// rows on purpose: without duplicates the id tiebreaker in the keyset
// comparison would never be exercised.
func seed(t *testing.T, n int) {
	t.Helper()

	if _, err := testDB.ExecContext(t.Context(), `TRUNCATE vms, nodes RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := testDB.ExecContext(t.Context(),
		`INSERT INTO nodes (name, ip) VALUES ('pve-test', '10.42.0.10')`,
	); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	_, err := testDB.ExecContext(t.Context(), `
		INSERT INTO vms (node_id, name, ram, status)
		SELECT 1,
		       'vm-' || lpad(g::text, 4, '0'),
		       512 * (1 + g % 4),
		       'running'
		FROM generate_series(1, $1) AS g`, n)
	if err != nil {
		t.Fatalf("insert vms: %v", err)
	}
}

// walkAll follows the cursor to the end and returns every id it saw.
func walkAll(t *testing.T, s *Store, sort SortKey, limit int) []int {
	t.Helper()

	var (
		ids    []int
		cursor *Cursor
	)
	for page := 0; ; page++ {
		if page > 100 {
			t.Fatal("pagination did not terminate")
		}

		got, err := s.ListVMs(t.Context(), ListVMsParams{
			Limit:  limit,
			Sort:   sort,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("list page %d: %v", page, err)
		}
		for _, v := range got.Items {
			ids = append(ids, v.ID)
		}
		if got.NextCursor == "" {
			return ids
		}
		c, err := DecodeCursor(got.NextCursor, sort)
		if err != nil {
			t.Fatalf("decode next cursor: %v", err)
		}
		cursor = &c
	}
}

func TestPaginationVisitsEveryRowExactlyOnce(t *testing.T) {
	seed(t, 97) // deliberately not a multiple of the page size
	s := New(testDB)

	for _, sort := range []SortKey{SortIDAsc, SortRAMAsc, SortRAMDesc, SortNameAsc} {
		t.Run(string(sort), func(t *testing.T) {
			ids := walkAll(t, s, sort, 10)

			if len(ids) != 97 {
				t.Fatalf("saw %d rows, want 97", len(ids))
			}
			seen := make(map[int]bool, len(ids))
			for _, id := range ids {
				if seen[id] {
					t.Fatalf("row %d returned twice", id)
				}
				seen[id] = true
			}
		})
	}
}

func TestPaginationHoldsOrdering(t *testing.T) {
	seed(t, 50)
	s := New(testDB)

	var (
		prevRAM = -1
		prevID  = -1
		cursor  *Cursor
	)
	for {
		got, err := s.ListVMs(t.Context(), ListVMsParams{
			Limit: 7, Sort: SortRAMDesc, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, v := range got.Items {
			if prevRAM != -1 {
				if v.RAM > prevRAM {
					t.Fatalf("ram went up: %d after %d", v.RAM, prevRAM)
				}
				if v.RAM == prevRAM && v.ID >= prevID {
					t.Fatalf("tiebreaker broken: id %d after %d at ram %d", v.ID, prevID, v.RAM)
				}
			}
			prevRAM, prevID = v.RAM, v.ID
		}
		if got.NextCursor == "" {
			return
		}
		c, err := DecodeCursor(got.NextCursor, SortRAMDesc)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		cursor = &c
	}
}

func TestNoCursorOnShortPage(t *testing.T) {
	seed(t, 5)
	s := New(testDB)

	got, err := s.ListVMs(t.Context(), ListVMsParams{Limit: 20, Sort: SortIDAsc})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got.Items) != 5 {
		t.Fatalf("got %d items, want 5", len(got.Items))
	}
	if got.NextCursor != "" {
		t.Error("short page should not issue a cursor")
	}
}

func TestNodeInventoryNotFound(t *testing.T) {
	seed(t, 3)
	s := New(testDB)

	if _, err := s.NodeInventory(t.Context(), 999999); err == nil {
		t.Fatal("expected an error for a missing node")
	}
}

func TestNodeInventoryReturnsVMs(t *testing.T) {
	seed(t, 4)
	s := New(testDB)

	node, err := s.NodeInventory(t.Context(), 1)
	if err != nil {
		t.Fatalf("node inventory: %v", err)
	}
	if len(node.VMs) != 4 {
		t.Fatalf("got %d vms, want 4", len(node.VMs))
	}
}
