package roster

import (
	"strings"
	"testing"
)

func TestParseFiveQuestionForm(t *testing.T) {
	input := "Timestamp,Display name,Email or club ID,Skill band,Estimated arrival time,Need a paddle?\n" +
		"2026/10/13,Ana, ANA@EXAMPLE.COM ,Intermediate,6:30 PM,Yes\n" +
		"2026/10/13,Ben,club-2,Beginner,,No\n" +
		"2026/10/13,Chi,club-3,Advanced,,\n"
	result, err := Parse(strings.NewReader(input))
	if err != nil || !result.OK() {
		t.Fatalf("parse five questions: %v / %+v", err, result)
	}
	if first := result.Entries[0]; first.PrivateRef != "ana@example.com" || first.EstimatedArrival != "6:30 PM" || first.NeedsPaddle == nil || !*first.NeedsPaddle {
		t.Errorf("first registration: %+v", first)
	}
	if second := result.Entries[1]; second.NeedsPaddle == nil || *second.NeedsPaddle {
		t.Errorf("No must be distinct from unanswered: %+v", second)
	}
	if third := result.Entries[2]; third.NeedsPaddle != nil || third.EstimatedArrival != "" {
		t.Errorf("optional answers: %+v", third)
	}
}

func TestParseOptionalRegistrationValidation(t *testing.T) {
	header := "Display name,Email or club ID,Skill band,Estimated arrival time,Need a paddle?\n"
	for _, rows := range []string{
		"Ana,a,Advanced,6 PM,Maybe\n",
		"Ana,a,Advanced," + strings.Repeat("x", 121) + ",Yes\n",
		"Ana,a,Advanced,6 PM,Yes\nAna,a,Advanced,6 PM,No\n",
		"Ana,a,Advanced,6 PM,Yes\nAna,a,Advanced,7 PM,Yes\n",
	} {
		result, err := Parse(strings.NewReader(header + rows))
		if err != nil || result.OK() || len(result.Errors) != 1 {
			t.Errorf("expected one invalid registration for %q: %v / %+v", rows, err, result)
		}
	}
	_, err := Parse(strings.NewReader("Name,Email,Skill,Arrival,Estimated arrival time\nAna,a,Advanced,6 PM,7 PM\n"))
	if err == nil || !strings.Contains(err.Error(), "two columns") {
		t.Fatalf("ambiguous arrival header: %v", err)
	}
}
