package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetAllPaginates(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing auth header")
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page not set: %s", r.URL.RawQuery)
		}
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/x?per_page=100&page=2>; rel="next", <%s/x?per_page=100&page=2>; rel="last"`, srv.URL, srv.URL))
			fmt.Fprint(w, `[{"login":"a"},{"login":"b"}]`)
		case "2":
			fmt.Fprint(w, `[{"login":"c"}]`)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	got, err := GetAll[Account](context.Background(), c, "/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].Login != "c" {
		t.Fatalf("got %+v", got)
	}
}

func TestRetriesOnRateLimit(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
			return
		}
		fmt.Fprint(w, `{"login":"me"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	var slept time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = d; return nil }
	u, err := c.AuthenticatedUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u.Login != "me" || calls != 2 {
		t.Fatalf("login=%q calls=%d", u.Login, calls)
	}
	if slept < 50*time.Minute {
		t.Fatalf("expected to wait until reset, slept %s", slept)
	}
}

func TestNotFoundIsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, err := c.AuthenticatedUser(context.Background())
	if StatusCode(err) != 404 || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
