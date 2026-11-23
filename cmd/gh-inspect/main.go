// Command gh-inspect enumerates GitHub repositories for orgs/users and reports
// who has access to each one and why.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"gh-inspect/internal/github"
	"gh-inspect/internal/inspect"
	"gh-inspect/internal/report"
)

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		orgs, users  listFlag
		allOrgs      = flag.Bool("all-orgs", false, "also scan every organization the token owner belongs to")
		format       = flag.String("format", "table", "output format: table, csv, json, html (interactive graph)")
		view         = flag.String("view", "repo", "row grouping for table/csv: repo (repo -> users) or user (user -> repos)")
		output       = flag.String("o", "", "write output to this file instead of stdout")
		apiURL       = flag.String("api-url", envOr("GITHUB_API_URL", github.DefaultBaseURL), "GitHub API base URL (GHES: https://HOST/api/v3)")
		noTeams      = flag.Bool("no-teams", false, "do not list teams with access to org repositories")
		expandTeams  = flag.Bool("expand-teams", true, "resolve team members so user access can be attributed to teams")
		noInvites    = flag.Bool("no-invites", false, "do not list pending invitations")
		skipArchived = flag.Bool("skip-archived", false, "skip archived repositories")
		skipForks    = flag.Bool("skip-forks", false, "skip forked repositories")
		repoRegex    = flag.String("repo", "", "only include repos whose owner/name matches this regex")
		concurrency  = flag.Int("concurrency", 8, "number of repositories inspected in parallel")
		quiet        = flag.Bool("q", false, "suppress progress logging on stderr")
	)
	flag.Var(&orgs, "org", "organization to scan (repeatable or comma-separated)")
	flag.Var(&users, "user", "user whose owned repos to scan (repeatable or comma-separated)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `gh-inspect: list GitHub repositories and who has access to them.

Usage:
  GITHUB_TOKEN=... gh-inspect [flags]

With no --org/--user, the token owner's own repositories are scanned.

Examples:
  gh-inspect --org my-org
  gh-inspect --org org-a,org-b --format csv -o access.csv
  gh-inspect --user octocat --view user
  gh-inspect --all-orgs --skip-archived --format json -o report.json
  gh-inspect --org my-org --format html          # interactive graph -> gh-access-report.html

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	token := firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN"))
	if token == "" {
		return fmt.Errorf("set GITHUB_TOKEN (or GH_TOKEN) to a token with repo and read:org access")
	}
	switch *format {
	case "table", "csv", "json", "html":
	default:
		return fmt.Errorf("invalid --format %q (want table, csv, json or html)", *format)
	}
	if *format == "html" && *output == "" {
		*output = "gh-access-report.html"
	}
	if *view != "repo" && *view != "user" {
		return fmt.Errorf("invalid --view %q (want repo or user)", *view)
	}
	var filter *regexp.Regexp
	if *repoRegex != "" {
		re, err := regexp.Compile(*repoRegex)
		if err != nil {
			return fmt.Errorf("invalid --repo regex: %w", err)
		}
		filter = re
	}

	logf := func(format string, args ...any) {
		if !*quiet {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gh := github.NewClient(*apiURL, token)
	gh.Logf = logf

	in := inspect.New(gh, inspect.Options{
		Orgs:           orgs,
		Users:          users,
		AllOrgs:        *allOrgs,
		IncludeTeams:   !*noTeams,
		ExpandTeams:    *expandTeams,
		IncludeInvites: !*noInvites,
		SkipArchived:   *skipArchived,
		SkipForks:      *skipForks,
		RepoFilter:     filter,
		Concurrency:    *concurrency,
		Logf:           logf,
	})
	rep, err := in.Run(ctx)
	if err != nil {
		return err
	}

	var w io.Writer = os.Stdout
	if *output != "" {
		f, err := os.Create(*output)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}

	switch *format {
	case "json":
		err = report.WriteJSON(w, rep)
	case "csv":
		err = report.WriteCSV(w, rep, *view)
	case "html":
		err = report.WriteHTML(w, rep)
	default:
		err = report.WriteTable(w, rep, *view)
	}
	if err != nil {
		return err
	}
	logf("\n%s", report.Summary(rep))
	if *output != "" {
		logf("report written to %s", *output)
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
