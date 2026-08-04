// Package store contains the domain models and all database access.
// Handlers never build SQL themselves; they call methods defined here.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Node is a physical or virtual host in the inventory.
type Node struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip"`
	VMs  []VM   `json:"vms"`
}

// VM is a guest running on a Node.
type VM struct {
	ID     int    `json:"id"`
	NodeID int    `json:"node_id"`
	Name   string `json:"name"`
	RAM    int    `json:"ram"`
	Status string `json:"status"`
}

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// ErrNotFound is returned when a requested record does not exist.
// Handlers translate it into 404 instead of guessing from zero values.
var ErrNotFound = fmt.Errorf("not found")

// NodeInventory returns a node together with its VMs.
// The LEFT JOIN means a node with no VMs still produces one row, so the
// nullable guest columns are scanned into sql.Null* and skipped when empty.
func (s *Store) NodeInventory(ctx context.Context, nodeID int) (Node, error) {
	const query = `
		SELECT n.id, n.name, n.ip,
		       v.id, v.node_id, v.name, v.ram, v.status
		FROM nodes n
		LEFT JOIN vms v ON v.node_id = n.id
		WHERE n.id = $1
		ORDER BY v.id`

	rows, err := s.db.QueryContext(ctx, query, nodeID)
	if err != nil {
		return Node{}, fmt.Errorf("query node inventory: %w", err)
	}
	defer rows.Close()

	var (
		node  Node
		found bool
	)
	node.VMs = make([]VM, 0)

	for rows.Next() {
		var (
			vID     sql.NullInt64
			vNodeID sql.NullInt64
			vName   sql.NullString
			vRAM    sql.NullInt64
			vStatus sql.NullString
		)

		if err := rows.Scan(
			&node.ID, &node.Name, &node.IP,
			&vID, &vNodeID, &vName, &vRAM, &vStatus,
		); err != nil {
			return Node{}, fmt.Errorf("scan node inventory: %w", err)
		}
		found = true

		if vID.Valid {
			node.VMs = append(node.VMs, VM{
				ID:     int(vID.Int64),
				NodeID: int(vNodeID.Int64),
				Name:   vName.String,
				RAM:    int(vRAM.Int64),
				Status: vStatus.String,
			})
		}
	}

	// Without this check a mid-iteration failure would silently return
	// a partial result with HTTP 200.
	if err := rows.Err(); err != nil {
		return Node{}, fmt.Errorf("iterate node inventory: %w", err)
	}

	if !found {
		return Node{}, ErrNotFound
	}
	return node, nil
}

// SortKey is a whitelisted ordering. User input is mapped onto one of these
// constants, so no part of the ORDER BY clause ever comes from the request.
type SortKey string

const (
	SortIDAsc   SortKey = "id_asc"
	SortRAMAsc  SortKey = "ram_asc"
	SortRAMDesc SortKey = "ram_desc"
	SortNameAsc SortKey = "name_asc"
)

// sortSpec describes how one ordering is expressed in SQL.
type sortSpec struct {
	// orderBy always ends with id so the ordering is total: sort columns are
	// not unique, and ties would otherwise shuffle between pages.
	orderBy string
	// where resumes the scan after the cursor position. It uses row-value
	// comparison, which Postgres can satisfy with a single index seek on the
	// matching composite index.
	where string
	// numericKey selects how the cursor key is bound as a query parameter.
	numericKey bool
}

var sortSpecs = map[SortKey]sortSpec{
	SortIDAsc:   {orderBy: "id ASC", where: "id > $1", numericKey: true},
	SortRAMAsc:  {orderBy: "ram ASC, id ASC", where: "(ram, id) > ($1, $2)", numericKey: true},
	SortRAMDesc: {orderBy: "ram DESC, id DESC", where: "(ram, id) < ($1, $2)", numericKey: true},
	SortNameAsc: {orderBy: "name ASC, id ASC", where: "(name, id) > ($1, $2)", numericKey: false},
}

// ParseSortKey maps a query-string value onto an allowed ordering.
func ParseSortKey(v string) (SortKey, bool) {
	if v == "" {
		return SortIDAsc, true
	}
	k := SortKey(v)
	if _, ok := sortSpecs[k]; ok {
		return k, true
	}
	return "", false
}

// ListVMsParams describes one page of VMs to fetch.
type ListVMsParams struct {
	Limit  int
	Sort   SortKey
	Cursor *Cursor // nil for the first page
}

// VMPage is one page of results plus the token for the page after it.
type VMPage struct {
	Items      []VM
	NextCursor string // empty when there is nothing more to fetch
}

// ListVMs returns one page of VMs using keyset pagination.
//
// OFFSET was the obvious alternative and is roughly 2000x slower on this
// dataset: Postgres still walks and discards every skipped row, so page 24500
// reads 490k index entries to return 20. The row-value comparison below turns
// that into a single index seek, and the cost stays flat however deep the
// client pages.
//
// The trade-off is that keyset cannot jump to an arbitrary page number — it
// only moves forward from a known position. That is the right shape for an
// API anyway, and it is why the response carries a cursor rather than a
// total page count.
func (s *Store) ListVMs(ctx context.Context, p ListVMsParams) (VMPage, error) {
	spec, ok := sortSpecs[p.Sort]
	if !ok {
		return VMPage{}, fmt.Errorf("unknown sort %q", p.Sort)
	}

	var (
		sb   strings.Builder
		args []any
	)

	sb.WriteString("SELECT id, node_id, name, ram, status FROM vms")

	if p.Cursor != nil {
		key, err := bindKey(p.Cursor.Key, spec.numericKey)
		if err != nil {
			return VMPage{}, err
		}

		sb.WriteString(" WHERE ")
		sb.WriteString(spec.where)

		// id_asc needs no tiebreaker: the sort column already is the key.
		if p.Sort == SortIDAsc {
			args = append(args, p.Cursor.ID)
		} else {
			args = append(args, key, p.Cursor.ID)
		}
	}

	sb.WriteString(" ORDER BY ")
	sb.WriteString(spec.orderBy)
	sb.WriteString(fmt.Sprintf(" LIMIT $%d", len(args)+1))
	args = append(args, p.Limit)

	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return VMPage{}, fmt.Errorf("query vms: %w", err)
	}
	defer rows.Close()

	vms := make([]VM, 0, p.Limit)
	for rows.Next() {
		var v VM
		if err := rows.Scan(&v.ID, &v.NodeID, &v.Name, &v.RAM, &v.Status); err != nil {
			return VMPage{}, fmt.Errorf("scan vm: %w", err)
		}
		vms = append(vms, v)
	}
	if err := rows.Err(); err != nil {
		return VMPage{}, fmt.Errorf("iterate vms: %w", err)
	}

	page := VMPage{Items: vms}

	// A short page means the end of the result set, so no cursor is issued.
	// A full page might still be the last one; the client discovers that by
	// following the cursor once more and getting an empty page back. Avoiding
	// that would cost an extra row fetch on every request.
	if len(vms) == p.Limit {
		last := vms[len(vms)-1]
		page.NextCursor = Cursor{
			Sort: p.Sort,
			Key:  sortKeyValue(p.Sort, last),
			ID:   last.ID,
		}.Encode()
	}

	return page, nil
}

// sortKeyValue extracts the value of whichever column the ordering uses.
func sortKeyValue(sort SortKey, v VM) string {
	switch sort {
	case SortNameAsc:
		return v.Name
	case SortRAMAsc, SortRAMDesc:
		return strconv.Itoa(v.RAM)
	default:
		return strconv.Itoa(v.ID)
	}
}

// bindKey converts the cursor key into the type the column expects, so the
// comparison uses the index instead of falling back to a cast.
func bindKey(key string, numeric bool) (any, error) {
	if !numeric {
		return key, nil
	}
	n, err := strconv.Atoi(key)
	if err != nil {
		return nil, fmt.Errorf("cursor key is not numeric")
	}
	return n, nil
}
