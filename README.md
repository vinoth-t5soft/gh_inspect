# gh-inspect

Enumerates GitHub repositories for organizations and/or users and reports **who has access to each repo, at what role, and why**. It's a single Go binary with no dependencies outside the standard library.

## Build

```sh
make build        # -> bin/gh-inspect
make test
```

## Token

Set `GITHUB_TOKEN` (or `GH_TOKEN`). Required scopes:

| Token type | Permissions |
|---|---|
| Classic PAT | `repo`, `read:org` (authorize for SSO if the org enforces SAML) |
| Fine-grained PAT / GitHub App | Repository: **Administration: read**, **Metadata: read**. Organization: **Members: read** |

Listing collaborators and invitations needs admin (or at least push) access on each repo. Repos you can't read are still listed, with an `ERROR` row, and the run carries on.

## Usage

```sh
# Your own repos (private ones included)
gh-inspect

# One or more orgs
gh-inspect --org acme
gh-inspect --org acme,globex --format csv -o access.csv

# Every org you belong to, pivoted by user
gh-inspect --all-orgs --view user

# Full structured report
gh-inspect --org acme --format json -o report.json

# Interactive graph (writes gh-access-report.html; open it in a browser)
gh-inspect --org acme --format html

# Filter
gh-inspect --org acme --skip-archived --skip-forks --repo '^acme/(api|web)'

# GitHub Enterprise Server
gh-inspect --api-url https://ghe.example.com/api/v3 --org acme
```

Other users' repos (`--user someone`) are limited to their **public** repos, because GitHub doesn't expose another user's private repos.

## Output

Each row is one (repository, principal) pair:

```
REPO      VISIBILITY  PRINCIPAL   TYPE        ROLE   VIA
acme/api  private     boss        User        admin  org-owner
acme/api  private     contractor  User        read   outside-collaborator
acme/api  private     dev         User        write  team:backend(write)
acme/api  private     member      User        read   org-base-permission
acme/api  private     backend     Team        write  team
acme/api  private     newbie      Invitation  read   pending-invite by boss
```

`ROLE` is the user's **effective** role (the highest of all grants). `VIA` explains where it comes from, and lists several sources joined with `;` when there are more than one:

| Source | Meaning |
|---|---|
| `owner` | Owner of a user-owned repo |
| `direct` | Added directly as a collaborator |
| `outside-collaborator` | Direct collaborator who isn't an org member |
| `team:<slug>(<role>)` | Member of a team that has access |
| `org-owner` | Org owner (implicit admin on every repo) |
| `org-base-permission` | None of the above, so access comes from the org's base permission |
| `team-or-org` | Shown with `--expand-teams=false`: indirect access, source not resolved |

## Interactive graph (`--format html`)

This writes a single self-contained HTML file. It loads nothing from external sites, so it works offline, and the access data stays in the file.

- **Graph:** repositories (blue squares), users (orange circles) and teams (green diamonds). Edge thickness shows role, with admin heaviest. Outside collaborators have a dashed ring, team memberships are dotted lines, and pending invites are dashed lines.
- **Filters along the top:** search, owner (org/user), visibility, role, access via (direct, outside collaborator, team, org owner, base permission, invite), and show/hide for team nodes, invites, archived repos and bots. Every filter also updates the stats, the graph and the table.
- **Click** a node to see everyone it connects to, with role and source. **Double-click** to focus on its neighbourhood. Drag to rearrange, scroll to zoom.
- **Access list table** below the graph is sortable, follows the filters, and can be downloaded as CSV.

Tip: org owners reach every repo and turn the graph into a hairball. Untick **Access via → Org owner** to see the structure underneath.

## Flags

Run `gh-inspect -h` for the full list. Key ones: `--org`, `--user`, `--all-orgs`, `--format table|csv|json|html`, `--view repo|user`, `-o FILE`, `--no-teams`, `--expand-teams`, `--no-invites`, `--skip-archived`, `--skip-forks`, `--repo REGEX`, `--concurrency N`, `-q`.

## Rate limits

The client follows pagination, waits out primary rate limits (`X-RateLimit-Reset`), backs off on secondary limits and `Retry-After`, and retries 5xx responses. An org repo costs about 3–5 API calls, and team-member lookups are cached per team, so large orgs (thousands of repos) fit inside the 5,000 requests/hour limit or wait for the reset automatically. Lower `--concurrency` if you hit secondary limits often.
