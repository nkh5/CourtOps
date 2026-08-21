package web_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/nicholashu/courtops/internal/store"
)

func TestDeleteFinishedEventEndpoint(t *testing.T) {
	c := newClient(t, nil)
	c.signIn()
	ctx := context.Background()
	event, err := c.st.CreateEvent(ctx, "Delete me", time.Now(), []string{"Court 1"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/events/" + itoa(event.ID) + "/delete"
	resp := c.post(path, url.Values{"confirmation": {"DELETE"}})
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	resp.Body.Close()
	if _, err := c.st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatal(err)
	}
	resp = c.post(path, url.Values{"confirmation": {"delete"}})
	wantStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()
	if _, err := c.st.GetEvent(ctx, event.ID); err != nil {
		t.Fatalf("invalid confirmation deleted event: %v", err)
	}
	resp = c.post(path, url.Values{"confirmation": {"DELETE"}})
	wantStatus(t, resp, http.StatusSeeOther)
	if got := resp.Header.Get("Location"); got != "/?notice=Event+deleted." {
		t.Errorf("redirect=%q", got)
	}
	resp.Body.Close()
	if _, err := c.st.GetEvent(ctx, event.ID); store.KindOf(err) != store.KindNotFound {
		t.Fatalf("event remained after confirmed deletion: %v", err)
	}
	resp = c.post(path, url.Values{"confirmation": {"DELETE"}})
	wantStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}
