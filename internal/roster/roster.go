// Package roster turns a Google Form response export into rows the store can
// import.
//
// Nothing here touches the database. Parsing and validation happen first and
// completely: either the whole file is good and gets imported in one
// transaction, or nothing is imported and the organizer sees the bad rows by
// number.
package roster

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nicholashu/courtops/internal/ratings"
)

// Limits for this version. A club roster is tens of people; anything larger is
// a mistake or a different product.
const (
	MaxBytes = 1 << 20 // 1 MB
	MaxRows  = 100
)

// Entry is one validated player from the file.
type Entry struct {
	Row              int    // 1-based line number in the file, for error messages
	DisplayName      string // shown publicly
	PrivateRef       string // trimmed and lower-cased; never shown publicly
	Band             string // beginner | intermediate | advanced
	Seed             int    // provisional starting rating for a new player
	EstimatedArrival string // event-specific planning note; organizer only
	NeedsPaddle      *bool  // nil means the optional question was not answered
}

// RowError is one rejected row.
type RowError struct {
	Row     int
	Message string
}

func (e RowError) Error() string { return fmt.Sprintf("row %d: %s", e.Row, e.Message) }

// Result is what the organizer sees in the preview.
type Result struct {
	Entries    []Entry
	Errors     []RowError
	Duplicates int // exact repeats of an earlier row, ignored rather than rejected
}

// OK reports whether the file is safe to import.
func (r Result) OK() bool { return len(r.Errors) == 0 && len(r.Entries) > 0 }

// ErrTooLarge is returned when the upload exceeds MaxBytes.
var ErrTooLarge = errors.New("roster: file is larger than 1 MB")

// ErrTooManyRows is returned when the file has more than MaxRows data rows.
var ErrTooManyRows = fmt.Errorf("roster: more than %d rows", MaxRows)

// Band seeds. These are local starting points, not a rating anyone earned.
var bandSeeds = map[string]int{
	"beginner":     ratings.SeedBeginner,
	"intermediate": ratings.SeedIntermediate,
	"advanced":     ratings.SeedAdvanced,
}

// Parse reads a CSV export and validates every row.
//
// A returned error means the file could not be read at all. Row-level problems
// come back in Result.Errors so that the organizer can fix the sheet and try
// again instead of guessing.
func Parse(r io.Reader) (Result, error) {
	// Read one byte past the limit so an oversized file is detected rather than
	// silently truncated into a "valid" roster.
	limited := io.LimitReader(r, MaxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return Result{}, fmt.Errorf("read upload: %w", err)
	}
	if len(data) > MaxBytes {
		return Result{}, ErrTooLarge
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return Result{}, errors.New("roster: the file is empty")
	}

	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
	cr.FieldsPerRecord = -1 // checked per row so one ragged line is not fatal
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return Result{}, fmt.Errorf("roster: cannot read the header row: %w", err)
	}
	cols, err := mapColumns(header)
	if err != nil {
		return Result{}, err
	}

	var (
		res  Result
		seen = make(map[string]Entry) // private ref -> first row that used it
		rows = 0                      // includes duplicates and malformed records
	)

	for {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err == nil && isBlank(record) {
			continue
		}
		rows++
		if rows > MaxRows {
			return Result{}, ErrTooManyRows
		}
		if err != nil {
			var parseErr *csv.ParseError
			if !errors.As(err, &parseErr) {
				return Result{}, fmt.Errorf("read CSV: %w", err)
			}
			res.Errors = append(res.Errors, RowError{Row: parseErr.StartLine, Message: readErrorMessage(err)})
			continue
		}
		row, _ := cr.FieldPos(0)

		entry, rowErr := parseRow(row, record, cols)
		if rowErr != nil {
			res.Errors = append(res.Errors, *rowErr)
			continue
		}

		if first, dup := seen[entry.PrivateRef]; dup {
			if first.DisplayName == entry.DisplayName && first.Band == entry.Band &&
				first.EstimatedArrival == entry.EstimatedArrival && sameAnswer(first.NeedsPaddle, entry.NeedsPaddle) {
				res.Duplicates++
				continue
			}
			res.Errors = append(res.Errors, RowError{
				Row: row,
				Message: fmt.Sprintf("identifier %q already appears on row %d with different details (%q / %s)",
					entry.PrivateRef, first.Row, first.DisplayName, first.Band),
			})
			continue
		}

		seen[entry.PrivateRef] = entry
		res.Entries = append(res.Entries, entry)
	}

	if len(res.Entries) == 0 && len(res.Errors) == 0 {
		res.Errors = append(res.Errors, RowError{Row: 0, Message: "the file has a header but no player rows"})
	}
	return res, nil
}

type columns struct {
	name    int
	ref     int
	band    int
	arrival int
	paddle  int
}

// mapColumns finds the three required columns and two optional questions. Google Forms exports the
// full question text as the header, so columns are matched by keyword rather
// than by an exact string.
func mapColumns(header []string) (columns, error) {
	cols := columns{name: -1, ref: -1, band: -1, arrival: -1, paddle: -1}
	assign := func(field *int, idx int, label string) error {
		if *field >= 0 {
			return fmt.Errorf("roster: two columns look like the %s column (%q and %q)",
				label, header[*field], header[idx])
		}
		*field = idx
		return nil
	}

	for i, h := range header {
		h := strings.ToLower(strings.TrimSpace(h))
		var err error
		switch {
		case h == "" || strings.Contains(h, "timestamp"):
			continue
		case strings.Contains(h, "arrival"):
			err = assign(&cols.arrival, i, "estimated arrival time")
		case strings.Contains(h, "paddle"):
			err = assign(&cols.paddle, i, "need a paddle")
		// Order matters: "skill level" also contains "name"-free words, and an
		// email column often contains "id".
		case strings.Contains(h, "skill") || strings.Contains(h, "level") || strings.Contains(h, "band"):
			err = assign(&cols.band, i, "skill band")
		case strings.Contains(h, "email") || strings.Contains(h, "club id") || strings.Contains(h, "identifier"):
			err = assign(&cols.ref, i, "identifier")
		case strings.Contains(h, "name"):
			err = assign(&cols.name, i, "display name")
		}
		if err != nil {
			return columns{}, err
		}
	}

	var missing []string
	if cols.name < 0 {
		missing = append(missing, "display name")
	}
	if cols.ref < 0 {
		missing = append(missing, "email or club ID")
	}
	if cols.band < 0 {
		missing = append(missing, "skill band")
	}
	if len(missing) > 0 {
		return columns{}, fmt.Errorf("roster: the file is missing a %s column", strings.Join(missing, ", a "))
	}
	return cols, nil
}

func parseRow(row int, record []string, cols columns) (Entry, *RowError) {
	get := func(i int) string {
		if i < 0 || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}

	entry, err := NewEntry(get(cols.name), get(cols.ref), get(cols.band), get(cols.arrival), get(cols.paddle))
	if err != nil {
		return Entry{}, &RowError{Row: row, Message: err.Error()}
	}
	entry.Row = row
	return entry, nil
}

// NewEntry validates both organizer additions and CSV rows with the same rules.
// Arrival stays text because form answers can be a local time or a planning note.
func NewEntry(name, ref, skill, arrival, paddle string) (Entry, error) {
	name = strings.TrimSpace(name)
	ref = strings.ToLower(strings.TrimSpace(ref))
	band := normaliseBand(skill)
	arrival = strings.TrimSpace(arrival)

	switch {
	case ref == "":
		return Entry{}, errors.New("missing email or club ID; this is how a returning player is recognised")
	case name == "":
		return Entry{}, errors.New("missing display name")
	case band == "":
		return Entry{}, fmt.Errorf("unknown skill band %q; expected beginner, intermediate or advanced", skill)
	case len(name) > 120:
		return Entry{}, errors.New("display name must be 120 bytes or fewer")
	case len(ref) > 254:
		return Entry{}, errors.New("email or club ID must be 254 bytes or fewer")
	case len(arrival) > 120:
		return Entry{}, errors.New("estimated arrival time must be 120 bytes or fewer")
	}
	var needsPaddle *bool
	switch strings.ToLower(strings.TrimSpace(paddle)) {
	case "":
	case "yes":
		answer := true
		needsPaddle = &answer
	case "no":
		answer := false
		needsPaddle = &answer
	default:
		return Entry{}, errors.New("need a paddle must be Yes, No, or left blank")
	}

	return Entry{
		DisplayName:      name,
		PrivateRef:       ref,
		Band:             band,
		Seed:             bandSeeds[band],
		EstimatedArrival: arrival,
		NeedsPaddle:      needsPaddle,
	}, nil
}

func sameAnswer(a, b *bool) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// normaliseBand accepts the form's own wording, e.g. "Advanced (competitive)".
func normaliseBand(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	matched := ""
	for band := range bandSeeds {
		if strings.Contains(s, band) {
			if matched != "" {
				return "" // ambiguous bands must not depend on map iteration order
			}
			matched = band
		}
	}
	if matched != "" {
		return matched
	}
	// A couple of common synonyms people type into a free-text field.
	switch {
	case strings.Contains(s, "new") || strings.Contains(s, "novice"):
		return "beginner"
	case strings.Contains(s, "social") || strings.Contains(s, "casual") || strings.Contains(s, "medium"):
		return "intermediate"
	case strings.Contains(s, "competitive") || strings.Contains(s, "expert"):
		return "advanced"
	}
	return ""
}

func isBlank(record []string) bool {
	for _, f := range record {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

func readErrorMessage(err error) string {
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) {
		return fmt.Sprintf("cannot be read as CSV (%v)", parseErr.Err)
	}
	return err.Error()
}

// SeedForBand exposes the band-to-seed mapping for display.
func SeedForBand(band string) int { return bandSeeds[band] }
