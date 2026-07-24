package roster

import (
	"errors"
	"strings"
	"testing"
)

const googleFormHeader = "Timestamp,What name should we show on the court board?,Your email address,Skill band\n"

func TestParseValidFile(t *testing.T) {
	in := googleFormHeader +
		"2026/09/12 6:01:22 PM,Ana Reyes,Ana.Reyes@example.com,Intermediate\n" +
		"2026/09/12 6:02:10 PM,Ben Osei,ben@example.com,Beginner (new to the sport)\n" +
		"2026/09/12 6:03:44 PM,Chi Nakamura,chi@example.com,Advanced\n"

	res, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if !res.OK() {
		t.Fatalf("expected a clean file, got errors: %v", res.Errors)
	}
	if len(res.Entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(res.Entries))
	}

	first := res.Entries[0]
	if first.PrivateRef != "ana.reyes@example.com" {
		t.Errorf("private ref = %q, want it trimmed and lower-cased", first.PrivateRef)
	}
	if first.DisplayName != "Ana Reyes" {
		t.Errorf("display name = %q", first.DisplayName)
	}
	if first.Band != "intermediate" || first.Seed != 1000 {
		t.Errorf("band/seed = %q/%d, want intermediate/1000", first.Band, first.Seed)
	}
	if res.Entries[1].Seed != 800 || res.Entries[2].Seed != 1200 {
		t.Errorf("seeds = %d/%d, want 800/1200", res.Entries[1].Seed, res.Entries[2].Seed)
	}
	if res.Entries[1].Row != 3 {
		t.Errorf("row = %d, want the file line number 3", res.Entries[1].Row)
	}
}

func TestParseRejectsBadRowsWithLineNumbers(t *testing.T) {
	in := "Name,Email,Skill\n" +
		"Ana,ana@example.com,Intermediate\n" +
		"Ben,,Beginner\n" + // missing identifier
		",chi@example.com,Advanced\n" + // missing name
		"Dee,dee@example.com,Sometimes\n" // unknown band

	res, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if res.OK() {
		t.Fatal("expected the file to be rejected")
	}
	if len(res.Errors) != 3 {
		t.Fatalf("got %d row errors, want 3: %v", len(res.Errors), res.Errors)
	}

	wantRows := []int{3, 4, 5}
	for i, want := range wantRows {
		if res.Errors[i].Row != want {
			t.Errorf("error %d on row %d, want row %d (%s)", i, res.Errors[i].Row, want, res.Errors[i].Message)
		}
	}
	if !strings.Contains(res.Errors[0].Message, "email or club ID") {
		t.Errorf("missing-identifier message unhelpful: %q", res.Errors[0].Message)
	}
	if !strings.Contains(res.Errors[2].Message, "Sometimes") {
		t.Errorf("unknown-band message should quote the value: %q", res.Errors[2].Message)
	}
}

func TestParseDuplicates(t *testing.T) {
	t.Run("exact repeat is ignored", func(t *testing.T) {
		in := "Name,Email,Skill\n" +
			"Ana,ana@example.com,Intermediate\n" +
			"Ana,ANA@example.com,Intermediate\n"

		res, err := Parse(strings.NewReader(in))
		if err != nil {
			t.Fatalf("Parse() error: %v", err)
		}
		if !res.OK() {
			t.Fatalf("expected acceptance, got %v", res.Errors)
		}
		if len(res.Entries) != 1 || res.Duplicates != 1 {
			t.Errorf("entries=%d duplicates=%d, want 1 and 1", len(res.Entries), res.Duplicates)
		}
	})

	t.Run("conflicting repeat is an error", func(t *testing.T) {
		in := "Name,Email,Skill\n" +
			"Ana Reyes,ana@example.com,Intermediate\n" +
			"Ana R,ana@example.com,Advanced\n"

		res, err := Parse(strings.NewReader(in))
		if err != nil {
			t.Fatalf("Parse() error: %v", err)
		}
		if res.OK() {
			t.Fatal("conflicting duplicate should be rejected")
		}
		if !strings.Contains(res.Errors[0].Message, "row 2") {
			t.Errorf("error should point at the first occurrence: %q", res.Errors[0].Message)
		}
	})
}

func TestParseHeaderProblems(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"no identifier column", "Name,Skill\nAna,Intermediate\n", "email or club ID"},
		{"no band column", "Name,Email\nAna,ana@example.com\n", "skill band"},
		{"no name column", "Email,Skill\nana@example.com,Intermediate\n", "display name"},
		{"ambiguous", "Name,Nickname,Email,Skill\nA,B,c@example.com,Advanced\n", "two columns"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.in))
			if err == nil {
				t.Fatal("expected a header error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestParseEmptyAndLimits(t *testing.T) {
	t.Run("empty file", func(t *testing.T) {
		if _, err := Parse(strings.NewReader("   \n")); err == nil {
			t.Fatal("expected an error for an empty file")
		}
	})

	t.Run("header only", func(t *testing.T) {
		res, err := Parse(strings.NewReader("Name,Email,Skill\n"))
		if err != nil {
			t.Fatalf("Parse() error: %v", err)
		}
		if res.OK() {
			t.Fatal("a header with no players is not importable")
		}
	})

	t.Run("too large", func(t *testing.T) {
		big := "Name,Email,Skill\n" + strings.Repeat("x", MaxBytes)
		_, err := Parse(strings.NewReader(big))
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("error = %v, want ErrTooLarge", err)
		}
	})

	t.Run("too many rows", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("Name,Email,Skill\n")
		for i := 0; i < MaxRows+5; i++ {
			b.WriteString("Player,p")
			b.WriteString(strings.Repeat("0", 1))
			b.WriteString(itoa(i))
			b.WriteString("@example.com,Intermediate\n")
		}
		if _, err := Parse(strings.NewReader(b.String())); !errors.Is(err, ErrTooManyRows) {
			t.Fatalf("error = %v, want ErrTooManyRows", err)
		}
	})
}

func TestParseIgnoresBlankLines(t *testing.T) {
	in := "Name,Email,Skill\n\nAna,ana@example.com,Intermediate\n,,\n"
	res, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if !res.OK() || len(res.Entries) != 1 {
		t.Fatalf("entries=%d errors=%v", len(res.Entries), res.Errors)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var out []byte
	for i > 0 {
		out = append([]byte{byte('0' + i%10)}, out...)
		i /= 10
	}
	return string(out)
}
