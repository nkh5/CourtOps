package roster

import (
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
