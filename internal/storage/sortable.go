package storage

import (
	"context"
	"database/sql"
	"time"
)

// SortableLayout is a fixed-width RFC3339 layout with nine fractional digits and a
// literal Z.
//
// Fixed width is what makes SQLite's text comparison agree with time order.
// time.RFC3339Nano drops trailing zeros, so a whole-second value is written as
// "2026-05-28T20:26:40Z" and one with a fraction as "2026-05-28T20:26:40.359Z".
// Comparing those as text puts the whole second AFTER the fractional one, because
// 'Z' (0x5A) sorts after '.' (0x2E), which inverts the order inside one second.
// Padding every value to the same width removes the ambiguity.
const SortableLayout = "2006-01-02T15:04:05.000000000Z"

// SortableTime formats t for any column that is ordered or compared as text.
func SortableTime(t time.Time) string { return t.UTC().Format(SortableLayout) }

// ParseSortableTime reads the fixed-width form and legacy RFC3339Nano rows, so a
// database written before the format was unified still loads.
func ParseSortableTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

// TimeColumn names one text column that holds a timestamp.
type TimeColumn struct{ Table, Column string }

// BackfillSortableTimes rewrites legacy RFC3339Nano timestamps into the fixed-width
// form. Rows written before the layout was unified keep a variable-width value, and
// those still compare and sort wrongly, so they are converted rather than tolerated.
// Values that do not parse are left alone: they were already unusable and rewriting
// them would invent data.
func BackfillSortableTimes(ctx context.Context, db *sql.DB, columns []TimeColumn) error {
	return Transaction(ctx, db, func(tx *sql.Tx) error {
		for _, column := range columns {
			type row struct {
				id    int64
				value string
			}
			pending := []row{}
			rows, err := tx.QueryContext(ctx, `SELECT rowid,`+column.Column+` FROM `+column.Table+` WHERE `+column.Column+`<>'' AND length(`+column.Column+`)<>?`, len(SortableLayout))
			if err != nil {
				return err
			}
			for rows.Next() {
				item := row{}
				if err = rows.Scan(&item.id, &item.value); err != nil {
					rows.Close()
					return err
				}
				pending = append(pending, item)
			}
			if err = rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			for _, item := range pending {
				at, err := ParseSortableTime(item.value)
				if err != nil {
					continue
				}
				if _, err = tx.ExecContext(ctx, `UPDATE `+column.Table+` SET `+column.Column+`=? WHERE rowid=?`, SortableTime(at), item.id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
