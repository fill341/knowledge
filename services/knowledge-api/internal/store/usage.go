package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Usage counters (ADR-9). One row per entry and day.

type UsageField string

const (
	UsageShown     UsageField = "shown"
	UsageDelivered UsageField = "delivered"
	UsageOpened    UsageField = "opened"
)

// RecordUsage adds one to a counter of today's row for each entry (duplicates in ids count once).
func (q *Queries) RecordUsage(ctx context.Context, ids []uuid.UUID, f UsageField) error {
	if len(ids) == 0 {
		return nil
	}
	switch f {
	case UsageShown, UsageDelivered, UsageOpened:
	default:
		return fmt.Errorf("unknown usage field %q", f)
	}
	_, err := q.db.Exec(ctx, `INSERT INTO entry_usage (entry_id, day, `+string(f)+`)
		SELECT DISTINCT id, current_date, 1 FROM unnest($1::uuid[]) id
		ON CONFLICT (entry_id, day) DO UPDATE SET `+string(f)+` = entry_usage.`+string(f)+` + 1`, ids)
	return err
}

type EntryUsage struct {
	Days      int        `json:"days"` // window length
	Shown     int        `json:"shown"`
	Delivered int        `json:"delivered"`
	Opened    int        `json:"opened"`
	LastDay   *time.Time `json:"last_day"`
}

// UsageOf sums an entry's counters over the last `days` days (today included).
func (q *Queries) UsageOf(ctx context.Context, id uuid.UUID, days int) (EntryUsage, error) {
	u := EntryUsage{Days: days}
	err := q.db.QueryRow(ctx, `SELECT coalesce(sum(shown), 0)::int, coalesce(sum(delivered), 0)::int,
			coalesce(sum(opened), 0)::int, max(day)
		FROM entry_usage WHERE entry_id = $1 AND day > current_date - $2::int`, id, days).
		Scan(&u.Shown, &u.Delivered, &u.Opened, &u.LastDay)
	return u, err
}

// UsageTrackingStart is the first day with any usage recorded, nil before the first one.
func (q *Queries) UsageTrackingStart(ctx context.Context) (*time.Time, error) {
	var d *time.Time
	err := q.db.QueryRow(ctx, `SELECT min(day) FROM entry_usage`).Scan(&d)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

// RepeatedEmptySearches counts distinct searches (by query hash) that returned nothing at least twice since a time.
func (q *Queries) RepeatedEmptySearches(ctx context.Context, since time.Time, principalID *uuid.UUID) (int, error) {
	var n int
	err := q.db.QueryRow(ctx, `SELECT count(*)::int FROM (
			SELECT details->>'query_hash' FROM audit_log
			WHERE action = 'search' AND (details->>'results')::int = 0 AND at >= $1
			  AND ($2::uuid IS NULL OR principal_id = $2)
			GROUP BY 1 HAVING count(*) >= 2) r`, since, principalID).Scan(&n)
	return n, err
}
