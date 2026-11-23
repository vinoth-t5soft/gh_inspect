package github

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// Account is a GitHub user, bot or organization.
type Account struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"` // "User", "Organization", "Bot"
}

// Repository is the subset of repository fields we use.
type Repository struct {
	Name          string  `json:"name"`
	FullName      string  `json:"full_name"`
	Owner         Account `json:"owner"`
	Private       bool    `json:"private"`
	Visibility    string  `json:"visibility"`
	Archived      bool    `json:"archived"`
	Fork          bool    `json:"fork"`
	HTMLURL       string  `json:"html_url"`
	DefaultBranch string  `json:"default_branch"`
}

// Collaborator is a user with access to a repository.
type Collaborator struct {
	Account
	SiteAdmin   bool            `json:"site_admin"`
	RoleName    string          `json:"role_name"`
	Permissions map[string]bool `json:"permissions"`
}

// Team is a team with access to a repository.
type Team struct {
	ID          int64           `json:"id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Permission  string          `json:"permission"`
	Permissions map[string]bool `json:"permissions"`
	Parent      *struct {
		Slug string `json:"slug"`
	} `json:"parent"`
}

// Invitation is a pending repository invitation.
type Invitation struct {
	ID          int64     `json:"id"`
	Invitee     *Account  `json:"invitee"`
	Inviter     *Account  `json:"inviter"`
	Permissions string    `json:"permissions"`
	CreatedAt   time.Time `json:"created_at"`
	Expired     bool      `json:"expired"`
}

func esc(s string) string { return url.PathEscape(s) }

// AuthenticatedUser returns the owner of the token.
func (c *Client) AuthenticatedUser(ctx context.Context) (*Account, error) {
	var a Account
	if err := c.GetJSON(ctx, "/user", &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// ListAuthenticatedUserOrgs lists orgs the token owner belongs to.
func (c *Client) ListAuthenticatedUserOrgs(ctx context.Context) ([]Account, error) {
	return GetAll[Account](ctx, c, "/user/orgs")
}

// ListOrgRepos lists every repository in an organization.
func (c *Client) ListOrgRepos(ctx context.Context, org string) ([]Repository, error) {
	return GetAll[Repository](ctx, c, fmt.Sprintf("/orgs/%s/repos?type=all", esc(org)))
}

// ListUserRepos lists repositories owned by a user. When self is true the
// authenticated endpoint is used so private repositories are included.
func (c *Client) ListUserRepos(ctx context.Context, user string, self bool) ([]Repository, error) {
	if self {
		return GetAll[Repository](ctx, c, "/user/repos?affiliation=owner&visibility=all")
	}
	return GetAll[Repository](ctx, c, fmt.Sprintf("/users/%s/repos?type=owner", esc(user)))
}

// ListCollaborators lists repository collaborators. affiliation is one of
// "all", "direct", "outside".
func (c *Client) ListCollaborators(ctx context.Context, owner, repo, affiliation string) ([]Collaborator, error) {
	return GetAll[Collaborator](ctx, c,
		fmt.Sprintf("/repos/%s/%s/collaborators?affiliation=%s", esc(owner), esc(repo), url.QueryEscape(affiliation)))
}

// ListRepoTeams lists teams with access to a repository.
func (c *Client) ListRepoTeams(ctx context.Context, owner, repo string) ([]Team, error) {
	return GetAll[Team](ctx, c, fmt.Sprintf("/repos/%s/%s/teams", esc(owner), esc(repo)))
}

// ListRepoInvitations lists pending invitations for a repository.
func (c *Client) ListRepoInvitations(ctx context.Context, owner, repo string) ([]Invitation, error) {
	return GetAll[Invitation](ctx, c, fmt.Sprintf("/repos/%s/%s/invitations", esc(owner), esc(repo)))
}

// ListTeamMembers lists members of a team (including child-team members).
func (c *Client) ListTeamMembers(ctx context.Context, org, slug string) ([]Account, error) {
	return GetAll[Account](ctx, c, fmt.Sprintf("/orgs/%s/teams/%s/members?role=all", esc(org), esc(slug)))
}

// ListOrgMembers lists org members filtered by role ("all", "admin", "member").
func (c *Client) ListOrgMembers(ctx context.Context, org, role string) ([]Account, error) {
	return GetAll[Account](ctx, c, fmt.Sprintf("/orgs/%s/members?role=%s", esc(org), url.QueryEscape(role)))
}
