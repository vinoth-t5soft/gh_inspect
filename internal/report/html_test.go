package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"gh-inspect/internal/inspect"
)

// sampleReport builds a deterministic, realistic-looking report.
func sampleReport() *inspect.Report {
	rng := rand.New(rand.NewSource(7))
	rep := &inspect.Report{GeneratedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), AuthenticatedAs: "sec-auditor", Orgs: []string{"acme", "acme-labs"}}
	owners := []string{"alice", "bob"}
	staff := []string{}
	for i := 0; i < 38; i++ {
		staff = append(staff, fmt.Sprintf("dev-%02d", i))
	}
	outside := []string{"contractor-jo", "agency-lee", "vendor-kim"}
	teams := map[string][]string{
		"backend": staff[0:10], "frontend": staff[10:18], "platform": staff[18:24],
		"data": staff[24:30], "security": staff[30:33],
	}
	teamNames := []string{"backend", "frontend", "platform", "data", "security"}
	vis := []string{"private", "private", "internal", "public"}
	for _, org := range rep.Orgs {
		n := 24
		if org == "acme-labs" {
			n = 10
		}
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("%s/svc-%02d", org, i)
			ra := inspect.RepoAccess{FullName: name, Owner: org, OwnerType: "Organization",
				Visibility: vis[rng.Intn(len(vis))], Archived: rng.Intn(9) == 0, URL: "https://github.com/" + name}
			access := map[string]inspect.UserAccess{}
			add := func(login, role, src string) {
				ua := access[login]
				if ua.Login == "" {
					ua = inspect.UserAccess{Login: login, Type: "User", Role: role}
				}
				ua.Sources = append(ua.Sources, src)
				access[login] = ua
			}
			for _, o := range owners {
				add(o, "admin", inspect.SourceOrgOwner)
			}
			for k := 0; k < 1+rng.Intn(2); k++ {
				t := teamNames[rng.Intn(len(teamNames))]
				role := []string{"write", "write", "maintain", "read"}[rng.Intn(4)]
				ra.Teams = append(ra.Teams, inspect.TeamAccess{Slug: t, Name: t, Role: role, Members: teams[t]})
				for _, m := range teams[t] {
					add(m, role, fmt.Sprintf("team:%s(%s)", t, role))
				}
			}
			if rng.Intn(3) == 0 {
				add(staff[rng.Intn(len(staff))], "admin", inspect.SourceDirect)
			}
			if rng.Intn(4) == 0 {
				add(outside[rng.Intn(len(outside))], []string{"read", "write"}[rng.Intn(2)], inspect.SourceOutside)
			}
			if rng.Intn(6) == 0 {
				add("deploy-bot[bot]", "write", inspect.SourceDirect)
				ua := access["deploy-bot[bot]"]
				ua.Type = "Bot"
				access["deploy-bot[bot]"] = ua
			}
			for _, ua := range access {
				ra.Collaborators = append(ra.Collaborators, ua)
			}
			if rng.Intn(8) == 0 {
				ra.Invitations = append(ra.Invitations, inspect.Invite{Login: "new-hire", Role: "write", InvitedBy: "alice"})
			}
			rep.Repos = append(rep.Repos, ra)
		}
	}
	rep.Repos = append(rep.Repos, inspect.RepoAccess{FullName: "acme/locked", Owner: "acme", OwnerType: "Organization",
		Visibility: "private", Errors: []string{"collaborators: 403 Must have admin rights to Repository."}})
	return rep
}

func TestWriteHTML(t *testing.T) {
	rep := sampleReport()
	rep.Repos[0].Collaborators[0].Login = "</script><img src=x onerror=alert(1)>"

	var buf bytes.Buffer
	if err := WriteHTML(&buf, rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, dataPlaceholder) {
		t.Fatal("placeholder not replaced")
	}
	if strings.Count(out, "</script>") != 2 {
		t.Fatalf("payload broke out of its script element: %d </script> tags", strings.Count(out, "</script>"))
	}
	start := strings.Index(out, `type="application/json">`) + len(`type="application/json">`)
	end := strings.Index(out[start:], "</script>")
	var back inspect.Report
	if err := json.Unmarshal([]byte(out[start:start+end]), &back); err != nil {
		t.Fatalf("embedded JSON invalid: %v", err)
	}
	if len(back.Repos) != len(rep.Repos) {
		t.Fatalf("repos: got %d want %d", len(back.Repos), len(rep.Repos))
	}

	// GH_INSPECT_DEMO_HTML=/path/demo.html go test ./internal/report -run TestWriteHTML
	if p := os.Getenv("GH_INSPECT_DEMO_HTML"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := WriteHTML(f, sampleReport()); err != nil {
			t.Fatal(err)
		}
	}
}
