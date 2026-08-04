// Package store contains the domain models and all database access.
// Handlers never build SQL themselves; they call methods defined here.
package store

import (
	"context"
	"database/sql"
	"fmt"
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

// SortOrder is a whitelisted ORDER BY clause. Values never come from user
// input directly, which is what keeps the query free of SQL injection.
type SortOrder string

const (
	SortIDAsc   SortOrder = "id ASC"
	SortRAMDesc SortOrder = "ram DESC, id DESC"
	SortRAMAsc  SortOrder = "ram ASC, id ASC"
	SortNameAsc SortOrder = "name ASC, id ASC"
)

// ParseSortOrder maps a query-string value to an allowed ORDER BY clause.
func ParseSortOrder(v string) (SortOrder, bool) {
	switch v {
	case "", "id_asc":
		return SortIDAsc, true
	case "ram_desc":
		return SortRAMDesc, true
	case "ram_asc":
		return SortRAMAsc, true
	case "name_asc":
		return SortNameAsc, true
	default:
		return "", false
	}
}

// ListVMsParams describes a page of VMs to fetch.
type ListVMsParams struct {
	Limit  int
	Offset int
	Sort   SortOrder
}

// ListVMs returns one page of VMs.
//
// NOTE: OFFSET degrades on deep pages because Postgres still reads and
// discards every skipped row. Next step is keyset pagination on
// (sort_column, id) with a matching composite index.
func (s *Store) ListVMs(ctx context.Context, p ListVMsParams) ([]VM, error) {
	query := "SELECT id, node_id, name, ram, status FROM vms ORDER BY " +
		string(p.Sort) + " LIMIT $1 OFFSET $2"

	rows, err := s.db.QueryContext(ctx, query, p.Limit, p.Offset)
	if err != nil {
		return nil, fmt.Errorf("query vms: %w", err)
	}
	defer rows.Close()

	vms := make([]VM, 0, p.Limit)
	for rows.Next() {
		var v VM
		if err := rows.Scan(&v.ID, &v.NodeID, &v.Name, &v.RAM, &v.Status); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vms = append(vms, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vms: %w", err)
	}

	return vms, nil
}
