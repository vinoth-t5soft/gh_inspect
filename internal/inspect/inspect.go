// Package inspect enumerates repositories and resolves who has access to each.
package inspect

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gh-inspect/internal/github"
)

// Access sources (why a principal has access).
const (
	SourceOwner          = "owner"
	SourceDirect         = "direct"
	SourceOutside        = "outside-collaborator"
	SourceOrgOwner       = "org-owner"
	SourceOrgBase        = "org-base-permission"
	SourceTeamPrefix     = "team:"
	SourceIndirectOrTeam = "team-or-org" // org repo, not direct, teams not expanded
)

// Options controls what is collected.
type Options struct {
	Orgs           []string
	Users          []string
	AllOrgs        bool // also scan every org the token owner belongs to
	IncludeTeams   bool
	ExpandTeams    bool // resolve team membership to attribute user access to teams
	IncludeInvites bool
	SkipArchived   bool
	SkipForks      bool
	RepoFilter     *regexp.Regexp
	Concurrency    int
	Logf           func(format string, args ...any)
}

// Report is the full result.
type Report struct {
	GeneratedAt     time.Time    `json:"generated_at"`
	AuthenticatedAs string       `json:"authenticated_as"`
	Orgs            []string     `json:"orgs,omitempty"`
	Users           []string     `json:"users,omitempty"`
	Repos           []RepoAccess `json:"repos"`
	Warnings        []string     `json:"warnings,omitempty"`
}

// RepoAccess describes everyone with access to one repository.
type RepoAccess struct {
	FullName      string       `json:"full_name"`
	Owner         string       `json:"owner"`
	OwnerType     string       `json:"owner_type"`
	Visibility    string       `json:"visibility"`
	Archived      bool         `json:"archived"`
	Fork          bool         `json:"fork"`
	URL           string       `json:"url"`
	Collaborators []UserAccess `json:"collaborators"`
	Teams         []TeamAccess `json:"teams,omitempty"`
	Invitations   []Invite     `json:"pending_invitations,omitempty"`
	Errors        []string     `json:"errors,omitempty"`
}

// UserAccess is one user's effective access to a repository.
type UserAccess struct {
	Login       string   `json:"login"`
	Type        string   `json:"type"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
	Sources     []string `json:"sources"`
	SiteAdmin   bool     `json:"site_admin,omitempty"`
}

// TeamAccess is a team's access to a repository.
type TeamAccess struct {
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	Parent  string   `json:"parent,omitempty"`
	Members []string `json:"members,omitempty"`
}

// Invite is a pending invitation.
type Invite struct {
	Login     string    `json:"login"`
	Role      string    `json:"role"`
	InvitedBy string    `json:"invited_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Expired   bool      `json:"expired"`
}

// Inspector runs the collection.
type Inspector struct {
	gh   *github.Client
	opts Options

	mu        sync.Mutex
	orgAdmins map[string]map[string]bool
	teamCache map[string]*teamEntry
	warnings  []string
}

type teamEntry struct {
	once    sync.Once
	members []string
	err     error
}

// New creates an Inspector.
func New(gh *github.Client, opts Options) *Inspector {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 8
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &Inspector{
		gh:        gh,
		opts:      opts,
		orgAdmins: map[string]map[string]bool{},
		teamCache: map[string]*teamEntry{},
	}
}

func (in *Inspector) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	in.opts.Logf("warning: %s", msg)
	in.mu.Lock()
	in.warnings = append(in.warnings, msg)
	in.mu.Unlock()
}

// Run performs the full enumeration.
func (in *Inspector) Run(ctx context.Context) (*Report, error) {
	me, err := in.gh.AuthenticatedUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticating: %w", err)
	}
	in.opts.Logf("authenticated as %s", me.Login)

	orgs := dedupe(in.opts.Orgs)
	if in.opts.AllOrgs {
		myOrgs, err := in.gh.ListAuthenticatedUserOrgs(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing your orgs: %w", err)
		}
		for _, o := range myOrgs {
			orgs = append(orgs, o.Login)
		}
		orgs = dedupe(orgs)
	}
	users := dedupe(in.opts.Users)
	if len(orgs) == 0 && len(users) == 0 {
		users = []string{me.Login}
	}

	repos, err := in.collectRepos(ctx, me.Login, orgs, users)
	if err != nil {
		return nil, err
	}
	in.opts.Logf("found %d repositories to inspect", len(repos))

	for _, org := range orgs {
		admins, err := in.gh.ListOrgMembers(ctx, org, "admin")
		if err != nil {
			in.warn("listing owners of org %s: %v", org, err)
			continue
		}
		set := map[string]bool{}
		for _, a := range admins {
			set[strings.ToLower(a.Login)] = true
		}
		in.orgAdmins[strings.ToLower(org)] = set
	}

	results := make([]RepoAccess, len(repos))
	sem := make(chan struct{}, in.opts.Concurrency)
	var wg sync.WaitGroup
	var done int
	var doneMu sync.Mutex
	for i, r := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = RepoAccess{FullName: r.FullName, Errors: []string{ctx.Err().Error()}}
				return
			}
			defer func() { <-sem }()
			results[i] = in.inspectRepo(ctx, r)
			doneMu.Lock()
			done++
			if done%25 == 0 || done == len(repos) {
				in.opts.Logf("inspected %d/%d repositories", done, len(repos))
			}
			doneMu.Unlock()
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		return strings.ToLower(results[i].FullName) < strings.ToLower(results[j].FullName)
	})
	return &Report{
		GeneratedAt:     time.Now().UTC(),
		AuthenticatedAs: me.Login,
		Orgs:            orgs,
		Users:           users,
		Repos:           results,
		Warnings:        in.warnings,
	}, nil
}

func (in *Inspector) collectRepos(ctx context.Context, me string, orgs, users []string) ([]github.Repository, error) {
	seen := map[string]bool{}
	var out []github.Repository
	add := func(rs []github.Repository) {
		for _, r := range rs {
			key := strings.ToLower(r.FullName)
			if seen[key] {
				continue
			}
			if in.opts.SkipArchived && r.Archived {
				continue
			}
			if in.opts.SkipForks && r.Fork {
				continue
			}
			if in.opts.RepoFilter != nil && !in.opts.RepoFilter.MatchString(r.FullName) {
				continue
			}
			seen[key] = true
			out = append(out, r)
		}
	}
	for _, org := range orgs {
		rs, err := in.gh.ListOrgRepos(ctx, org)
		if err != nil {
			return nil, fmt.Errorf("listing repos for org %s: %w", org, err)
		}
		in.opts.Logf("org %s: %d repositories", org, len(rs))
		add(rs)
	}
	for _, u := range users {
		self := strings.EqualFold(u, me)
		rs, err := in.gh.ListUserRepos(ctx, u, self)
		if err != nil {
			return nil, fmt.Errorf("listing repos for user %s: %w", u, err)
		}
		if !self {
			in.warn("user %s is not the token owner; only their public repositories are visible", u)
		}
		in.opts.Logf("user %s: %d repositories", u, len(rs))
		add(rs)
	}
	return out, nil
}

func (in *Inspector) inspectRepo(ctx context.Context, r github.Repository) RepoAccess {
	ra := RepoAccess{
		FullName:   r.FullName,
		Owner:      r.Owner.Login,
		OwnerType:  r.Owner.Type,
		Visibility: visibility(r),
		Archived:   r.Archived,
		Fork:       r.Fork,
		URL:        r.HTMLURL,
	}
	owner, name := r.Owner.Login, r.Name
	isOrg := r.Owner.Type == "Organization"

	all, err := in.gh.ListCollaborators(ctx, owner, name, "all")
	if err != nil {
		ra.Errors = append(ra.Errors, fmt.Sprintf("collaborators: %v", err))
		return ra
	}

	direct := map[string]bool{}
	outside := map[string]bool{}
	teamSources := map[string][]string{} // login -> ["team:slug(role)"]

	if isOrg {
		if d, err := in.gh.ListCollaborators(ctx, owner, name, "direct"); err != nil {
			ra.Errors = append(ra.Errors, fmt.Sprintf("direct collaborators: %v", err))
		} else {
			for _, c := range d {
				direct[strings.ToLower(c.Login)] = true
			}
		}
		if o, err := in.gh.ListCollaborators(ctx, owner, name, "outside"); err != nil {
			ra.Errors = append(ra.Errors, fmt.Sprintf("outside collaborators: %v", err))
		} else {
			for _, c := range o {
				outside[strings.ToLower(c.Login)] = true
			}
		}

		if in.opts.IncludeTeams || in.opts.ExpandTeams {
			teams, err := in.gh.ListRepoTeams(ctx, owner, name)
			if err != nil {
				ra.Errors = append(ra.Errors, fmt.Sprintf("teams: %v", err))
			}
			for _, t := range teams {
				ta := TeamAccess{Slug: t.Slug, Name: t.Name, Role: normalizeRole(t.Permission)}
				if t.Parent != nil {
					ta.Parent = t.Parent.Slug
				}
				if in.opts.ExpandTeams {
					members, err := in.teamMembers(ctx, owner, t.Slug)
					if err != nil {
						ra.Errors = append(ra.Errors, fmt.Sprintf("members of team %s: %v", t.Slug, err))
					}
					ta.Members = members
					src := fmt.Sprintf("%s%s(%s)", SourceTeamPrefix, t.Slug, ta.Role)
					for _, m := range members {
						k := strings.ToLower(m)
						teamSources[k] = append(teamSources[k], src)
					}
				}
				ra.Teams = append(ra.Teams, ta)
			}
		}
	}

	if in.opts.IncludeInvites {
		invs, err := in.gh.ListRepoInvitations(ctx, owner, name)
		if err != nil {
			ra.Errors = append(ra.Errors, fmt.Sprintf("invitations: %v", err))
		}
		for _, iv := range invs {
			inv := Invite{Role: normalizeRole(iv.Permissions), CreatedAt: iv.CreatedAt, Expired: iv.Expired}
			if iv.Invitee != nil {
				inv.Login = iv.Invitee.Login
			}
			if iv.Inviter != nil {
				inv.InvitedBy = iv.Inviter.Login
			}
			ra.Invitations = append(ra.Invitations, inv)
		}
	}

	admins := in.orgAdmins[strings.ToLower(owner)]
	teamsResolved := in.opts.ExpandTeams
	for _, c := range all {
		k := strings.ToLower(c.Login)
		var sources []string
		switch {
		case !isOrg:
			if strings.EqualFold(c.Login, owner) {
				sources = append(sources, SourceOwner)
			} else {
				sources = append(sources, SourceDirect)
			}
		default:
			if outside[k] {
				sources = append(sources, SourceOutside)
			} else if direct[k] {
				sources = append(sources, SourceDirect)
			}
			sources = append(sources, teamSources[k]...)
			if admins[k] {
				sources = append(sources, SourceOrgOwner)
			}
			if len(sources) == 0 {
				if teamsResolved {
					sources = append(sources, SourceOrgBase)
				} else {
					sources = append(sources, SourceIndirectOrTeam)
				}
			}
		}
		role := c.RoleName
		if role == "" {
			role = roleFromPermissions(c.Permissions)
		}
		ra.Collaborators = append(ra.Collaborators, UserAccess{
			Login:       c.Login,
			Type:        c.Type,
			Role:        role,
			Permissions: grantedPermissions(c.Permissions),
			Sources:     sources,
			SiteAdmin:   c.SiteAdmin,
		})
	}
	sort.Slice(ra.Collaborators, func(i, j int) bool {
		return strings.ToLower(ra.Collaborators[i].Login) < strings.ToLower(ra.Collaborators[j].Login)
	})
	return ra
}

func (in *Inspector) teamMembers(ctx context.Context, org, slug string) ([]string, error) {
	key := strings.ToLower(org + "/" + slug)
	in.mu.Lock()
	e, ok := in.teamCache[key]
	if !ok {
		e = &teamEntry{}
		in.teamCache[key] = e
	}
	in.mu.Unlock()
	e.once.Do(func() {
		accts, err := in.gh.ListTeamMembers(ctx, org, slug)
		e.err = err
		for _, a := range accts {
			e.members = append(e.members, a.Login)
		}
		sort.Strings(e.members)
	})
	return e.members, e.err
}

var permOrder = []string{"admin", "maintain", "push", "triage", "pull"}

func grantedPermissions(p map[string]bool) []string {
	var out []string
	for _, k := range permOrder {
		if p[k] {
			out = append(out, k)
		}
	}
	return out
}

func roleFromPermissions(p map[string]bool) string {
	switch {
	case p["admin"]:
		return "admin"
	case p["maintain"]:
		return "maintain"
	case p["push"]:
		return "write"
	case p["triage"]:
		return "triage"
	case p["pull"]:
		return "read"
	}
	return "none"
}

// normalizeRole maps legacy permission names to UI role names.
func normalizeRole(p string) string {
	switch p {
	case "pull":
		return "read"
	case "push":
		return "write"
	}
	return p
}

func visibility(r github.Repository) string {
	if r.Visibility != "" {
		return r.Visibility
	}
	if r.Private {
		return "private"
	}
	return "public"
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}
