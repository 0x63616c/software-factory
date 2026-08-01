package store

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// pgUUID parses a Temporal run id (a UUID's string form) into pgtype.UUID for
// a query argument.
func pgUUID(runID string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(runID); err != nil {
		return pgtype.UUID{}, fmt.Errorf("run id %q is not a UUID: %w", runID, err)
	}
	return u, nil
}

// runIDString renders a stored run id back into the string form
// internal/work.StageKey.RunID and internal/work.Run carry.
func runIDString(u pgtype.UUID) string {
	// pgtype.UUID.Value never errors for a valid UUID; MustEncode-style use is
	// safe here because the column is NOT NULL and every write went through
	// pgUUID above.
	text, _ := u.Value()
	s, _ := text.(string)
	return s
}

// pgTimestamp converts a UTC instant to a required (NOT NULL) timestamptz
// column value.
func pgTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t.UTC().Truncate(time.Microsecond), Valid: true}
}

// pgOptionalTimestamp converts a zero instant to SQL NULL and any other instant
// to its UTC microsecond representation.
func pgOptionalTimestamp(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgTimestamp(t)
}

// timeFromPg converts a required timestamptz column back to time.Time.
func timeFromPg(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

// pgOptionalText converts a string to a nullable text column value. An empty
// string stores SQL NULL, which is how BreakerReason and Run.Outcome
// represent "not set" rather than the empty string itself.
func pgOptionalText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// textFromPg converts a nullable text column back to a string, "" for NULL.
func textFromPg(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
