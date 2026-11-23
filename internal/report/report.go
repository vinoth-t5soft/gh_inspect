// Package report renders an inspect.Report as table, CSV or JSON.
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"gh-inspect/internal/inspect"
)

// Row is one flattened (repo, principal) access entry.
type Row struct {
	Repo          string
	Visibility    string
	Archived      bool
	Principal     string
	PrincipalType string // User, Bot, Team, Invitation
	Role          string
	Via           string
}

// Rows flattens the report. Teams are included as their own principals and
// pending invitations as "Invitation" principals.
func Rows(r *inspect.Report) []Row {
	var rows []Row
	for _, repo := range r.Repos {
		base := Row{Repo: repo.FullName, Visibility: repo.Visibility, Archived: repo.Archived}
		for _, c := range repo.Collaborators {
			row := base
			row.Principal, row.PrincipalType, row.Role = c.Login, c.Type, c.Role
			row.Via = strings.Join(c.Sources, ";")
			rows = append(rows, row)
		}
		for _, t := range repo.Teams {
			row := base
			row.Principal, row.PrincipalType, row.Role = t.Slug, "Team", t.Role
			row.Via = "team"
			if t.Parent != "" {
				row.Via = "team (parent: " + t.Parent + ")"
			}
			rows = append(rows, row)
		}
		for _, iv := range repo.Invitations {
			row := base
			row.Principal, row.PrincipalType, row.Role = iv.Login, "Invitation", iv.Role
			row.Via = "pending-invite"
			if iv.InvitedBy != "" {
				row.Via += " by " + iv.InvitedBy
			}
			if iv.Expired {
				row.Via += " (expired)"
			}
			rows = append(rows, row)
		}
		if len(repo.Errors) > 0 {
			row := base
			row.Principal, row.PrincipalType = "-", "ERROR"
			row.Via = strings.Join(repo.Errors, "; ")
			rows = append(rows, row)
		}
	}
	return rows
}

func sortRows(rows []Row, view string) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if view == "user" {
			if !strings.EqualFold(a.Principal, b.Principal) {
				return strings.ToLower(a.Principal) < strings.ToLower(b.Principal)
			}
			return strings.ToLower(a.Repo) < strings.ToLower(b.Repo)
		}
		if !strings.EqualFold(a.Repo, b.Repo) {
			return strings.ToLower(a.Repo) < strings.ToLower(b.Repo)
		}
		if a.PrincipalType != b.PrincipalType {
			return typeRank(a.PrincipalType) < typeRank(b.PrincipalType)
		}
		return strings.ToLower(a.Principal) < strings.ToLower(b.Principal)
	})
}

func typeRank(t string) int {
	switch t {
	case "User", "Bot":
		return 0
	case "Team":
		return 1
	case "Invitation":
		return 2
	}
	return 3
}

func header(view string) []string {
	if view == "user" {
		return []string{"PRINCIPAL", "TYPE", "REPO", "VISIBILITY", "ROLE", "VIA"}
	}
	return []string{"REPO", "VISIBILITY", "PRINCIPAL", "TYPE", "ROLE", "VIA"}
}

func record(r Row, view string) []string {
	vis := r.Visibility
	if r.Archived {
		vis += " (archived)"
	}
	if view == "user" {
		return []string{r.Principal, r.PrincipalType, r.Repo, vis, r.Role, r.Via}
	}
	return []string{r.Repo, vis, r.Principal, r.PrincipalType, r.Role, r.Via}
}

// WriteTable writes an aligned text table.
func WriteTable(w io.Writer, r *inspect.Report, view string) error {
	rows := Rows(r)
	sortRows(rows, view)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header(view), "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(record(row, view), "\t"))
	}
	return tw.Flush()
}

// WriteCSV writes CSV with a header row.
func WriteCSV(w io.Writer, r *inspect.Report, view string) error {
	rows := Rows(r)
	sortRows(rows, view)
	cw := csv.NewWriter(w)
	h := header(view)
	for i := range h {
		h[i] = strings.ToLower(h[i])
	}
	if err := cw.Write(h); err != nil {
		return err
	}
	for _, row := range rows {
		if err := cw.Write(record(row, view)); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteJSON writes the full structured report.
func WriteJSON(w io.Writer, r *inspect.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Summary returns a one-paragraph human summary.
func Summary(r *inspect.Report) string {
	users := map[string]bool{}
	outside := map[string]bool{}
	admins := map[string]bool{}
	errs := 0
	for _, repo := range r.Repos {
		if len(repo.Errors) > 0 {
			errs++
		}
		for _, c := range repo.Collaborators {
			users[strings.ToLower(c.Login)] = true
			if c.Role == "admin" {
				admins[strings.ToLower(c.Login)] = true
			}
			for _, s := range c.Sources {
				if s == inspect.SourceOutside {
					outside[strings.ToLower(c.Login)] = true
				}
			}
		}
	}
	return fmt.Sprintf("%d repositories, %d distinct users (%d with admin on at least one repo, %d outside collaborators), %d repositories with errors, %d warnings",
		len(r.Repos), len(users), len(admins), len(outside), errs, len(r.Warnings))
}
