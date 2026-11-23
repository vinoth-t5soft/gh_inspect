package inspect

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gh-inspect/internal/github"
)

// fakeGitHub serves canned responses keyed by path + affiliation/role query.
func fakeGitHub(t *testing.T) *httptest.Server {
	routes := map[string]string{
		"/user": `{"login":"me","type":"User"}`,
		"/orgs/acme/repos": `[
			{"name":"api","full_name":"acme/api","owner":{"login":"acme","type":"Organization"},"private":true,"visibility":"private"},
			{"name":"old","full_name":"acme/old","owner":{"login":"acme","type":"Organization"},"archived":true,"visibility":"internal"}]`,
		"/orgs/acme/members?role=admin": `[{"login":"boss"}]`,
		"/repos/acme/api/collaborators?affiliation=all": `[
			{"login":"boss","type":"User","role_name":"admin","permissions":{"admin":true,"maintain":true,"push":true,"triage":true,"pull":true}},
			{"login":"dev","type":"User","role_name":"write","permissions":{"push":true,"triage":true,"pull":true}},
			{"login":"contractor","type":"User","role_name":"read","permissions":{"pull":true}},
			{"login":"member","type":"User","role_name":"read","permissions":{"pull":true}}]`,
		"/repos/acme/api/collaborators?affiliation=direct":  `[{"login":"contractor"}]`,
		"/repos/acme/api/collaborators?affiliation=outside": `[{"login":"contractor"}]`,
		"/repos/acme/api/teams":                             `[{"slug":"backend","name":"Backend","permission":"push"}]`,
		"/orgs/acme/teams/backend/members?role=all":         `[{"login":"dev"}]`,
		"/repos/acme/api/invitations":                       `[{"invitee":{"login":"newbie"},"inviter":{"login":"boss"},"permissions":"pull"}]`,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		q := r.URL.Query()
		for _, p := range []string{"affiliation", "role"} {
			if v := q.Get(p); v != "" {
				key += "?" + p + "=" + v
			}
		}
		body, ok := routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		w.Write([]byte(body))
	}))
}

func TestRunAttributesAccess(t *testing.T) {
	srv := fakeGitHub(t)
	defer srv.Close()

	in := New(github.NewClient(srv.URL, "tok"), Options{
		Orgs: []string{"acme"}, IncludeTeams: true, ExpandTeams: true, IncludeInvites: true, SkipArchived: true,
	})
	rep, err := in.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Repos) != 1 || rep.Repos[0].FullName != "acme/api" {
		t.Fatalf("expected only acme/api (archived skipped), got %+v", rep.Repos)
	}
	repo := rep.Repos[0]
	if len(repo.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", repo.Errors)
	}

	want := map[string]string{
		"boss":       "org-owner",
		"dev":        "team:backend(write)",
		"contractor": "outside-collaborator",
		"member":     "org-base-permission",
	}
	for _, c := range repo.Collaborators {
		got := strings.Join(c.Sources, ";")
		if got != want[c.Login] {
			t.Errorf("%s: sources=%q want %q", c.Login, got, want[c.Login])
		}
	}
	if len(repo.Teams) != 1 || repo.Teams[0].Role != "write" || repo.Teams[0].Members[0] != "dev" {
		t.Errorf("teams: %+v", repo.Teams)
	}
	if len(repo.Invitations) != 1 || repo.Invitations[0].Login != "newbie" || repo.Invitations[0].Role != "read" {
		t.Errorf("invitations: %+v", repo.Invitations)
	}
}

func TestRepoErrorsAreRecordedNotFatal(t *testing.T) {
	srv := fakeGitHub(t)
	defer srv.Close()

	// acme/old has no collaborator route -> 404, should be recorded on the repo.
	in := New(github.NewClient(srv.URL, "tok"), Options{Orgs: []string{"acme"}})
	rep, err := in.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, r := range rep.Repos {
		if r.FullName == "acme/old" {
			buf.WriteString(strings.Join(r.Errors, ";"))
		}
	}
	if !strings.Contains(buf.String(), "404") {
		t.Fatalf("expected 404 error recorded for acme/old, got %q", buf.String())
	}
}
