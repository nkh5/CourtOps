package web_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicholashu/courtops/internal/roster"
	"github.com/nicholashu/courtops/internal/store"
)

func TestPublicFinishedResultsAndPlayerHistory(t *testing.T) {
	c := newClient(t, nil)
	ctx := context.Background()
	e, err := c.st.CreateEvent(ctx, "Final <night>", time.Now(), []string{"Court 1"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := roster.NewEntry("Alex <script>", "secret@example.com", "intermediate", "private arrival", "yes")
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.st.AddPlayer(ctx, e.ID, entry, true)
	if err != nil {
		t.Fatal(err)
	}
	r := c.get(fmt.Sprintf("/results/%d", e.ID))
	wantStatus(t, r, http.StatusUnprocessableEntity)
	r.Body.Close()
	if _, err = c.st.SetEventState(ctx, e.ID, store.EventFinished); err != nil {
		t.Fatal(err)
	}
	r = c.get(fmt.Sprintf("/board/%d", e.ID))
	wantStatus(t, r, http.StatusSeeOther)
	if r.Header.Get("Location") != fmt.Sprintf("/results/%d", e.ID) {
		t.Fatal("board did not redirect to results")
	}
	r.Body.Close()
	r = c.get(fmt.Sprintf("/board/%d?fragment=1", e.ID))
	wantStatus(t, r, http.StatusOK)
	if r.Header.Get("X-CourtOps-Results") == "" {
		t.Fatal("live board cannot transition to results")
	}
	r.Body.Close()
	r = c.get(fmt.Sprintf("/results/%d", e.ID))
	wantStatus(t, r, http.StatusOK)
	html := body(t, r)
	for _, want := range []string{"Podium", "Find Your Results", "Not recorded", "Unrated", "Alex &lt;script&gt;"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, private := range []string{"secret@example.com", "private arrival", "<script>"} {
		if strings.Contains(html, private) {
			t.Errorf("unsafe result %s", private)
		}
	}
	r = c.get(fmt.Sprintf("/results/%d?q=nobody", e.ID))
	if !strings.Contains(body(t, r), "No players found") {
		t.Fatal("search ignored")
	}
	r = c.get(fmt.Sprintf("/players/%d", p.PlayerID))
	wantStatus(t, r, http.StatusOK)
	html = body(t, r)
	if !strings.Contains(html, "Match History") || strings.Contains(html, "secret@example.com") {
		t.Fatal("bad public profile")
	}
	r = c.get(fmt.Sprintf("/players/%d?page=-1", p.PlayerID))
	wantStatus(t, r, http.StatusBadRequest)
	r.Body.Close()
	r = c.get("/players/999999")
	wantStatus(t, r, http.StatusNotFound)
	r.Body.Close()
	r = c.get("/")
	if !strings.Contains(body(t, r), fmt.Sprintf(`href="/results/%d"`, e.ID)) {
		t.Fatal("home missing results link")
	}
}
