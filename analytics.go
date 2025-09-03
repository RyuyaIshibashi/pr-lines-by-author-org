package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const endpoint = "https://api.github.com/graphql"

type graphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type prNode struct {
	Number      int       `json:"number"`
	MergedAt    time.Time `json:"mergedAt"`
	Additions   int       `json:"additions"`
	Deletions   int       `json:"deletions"`
	BaseRefName string    `json:"baseRefName"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
}

type prResp struct {
	Data struct {
		Repository struct {
			PullRequests struct {
				PageInfo pageInfo `json:"pageInfo"`
				Nodes    []prNode `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type reposResp struct {
	Data struct {
		Organization struct {
			Repositories struct {
				PageInfo pageInfo `json:"pageInfo"`
				Nodes    []struct {
					Name       string `json:"name"`
					IsFork     bool   `json:"isFork"`
					IsArchived bool   `json:"isArchived"`
					IsPrivate  bool   `json:"isPrivate"`
				} `json:"nodes"`
			} `json:"repositories"`
		} `json:"organization"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type agg struct {
	Additions int
	Deletions int
	PRs       int
}

func mustParseTimeOrZero(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	fmt.Fprintf(os.Stderr, "WARN: cannot parse time %q, ignoring filter\n", s)
	return time.Time{}
}

func inRange(t, since, until time.Time) bool {
	if !since.IsZero() && t.Before(since) {
		return false
	}
	if !until.IsZero() && t.After(until) {
		return false
	}
	return true
}

func doGraphQL(token string, q string, vars map[string]interface{}) ([]byte, error) {
	body, _ := json.Marshal(graphQLRequest{Query: q, Variables: vars})
	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(300*(attempt+1)) * time.Millisecond)
			continue
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			lastErr = fmt.Errorf("server %d: %s", resp.StatusCode, string(b))
			time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
			continue
		}
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return nil, fmt.Errorf("auth/rate error %d: %s", resp.StatusCode, string(b))
		}
		return b, nil
	}
	return nil, lastErr
}

// visibility: all|public|private
func fetchOrgRepos(token, org string, includeForks, includeArchived bool, visibility string, maxRepos int) ([]string, error) {
	const reposQuery = `
query($org:String!, $cursor:String, $privacy: RepositoryPrivacy) {
  organization(login:$org) {
    repositories(
      first:100,
      after:$cursor,
      orderBy:{field: NAME, direction: ASC},
      privacy:$privacy
    ) {
      pageInfo { hasNextPage endCursor }
      nodes { name isFork isArchived isPrivate }
    }
  }
}`
	// privacy は単一値。all の場合は nil を渡す（未指定）。
	var privacy *string
	switch strings.ToLower(visibility) {
	case "public":
		v := "PUBLIC"
		privacy = &v
	case "private":
		v := "PRIVATE"
		privacy = &v
	case "", "all":
		privacy = nil
	default:
		fmt.Fprintf(os.Stderr, "WARN: unknown visibility %q -> using all\n", visibility)
		privacy = nil
	}

	var repos []string
	var cursor *string
	for {
		vars := map[string]interface{}{
			"org": org,
			"cursor": func() interface{} {
				if cursor == nil {
					return nil
				}
				return *cursor
			}(),
			"privacy": func() interface{} {
				if privacy == nil {
					return nil
				}
				return *privacy
			}(),
		}
		b, err := doGraphQL(token, reposQuery, vars)
		if err != nil {
			return nil, err
		}
		var out reposResp
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, err
		}
		if len(out.Errors) > 0 {
			msgs := make([]string, 0, len(out.Errors))
			for _, e := range out.Errors {
				msgs = append(msgs, e.Message)
			}
			return nil, errors.New(strings.Join(msgs, "; "))
		}
		nodes := out.Data.Organization.Repositories.Nodes
		for _, n := range nodes {
			if !includeForks && n.IsFork {
				continue
			}
			if !includeArchived && n.IsArchived {
				continue
			}
			repos = append(repos, n.Name)
			if maxRepos > 0 && len(repos) >= maxRepos {
				return repos, nil
			}
		}
		if out.Data.Organization.Repositories.PageInfo.HasNextPage {
			next := out.Data.Organization.Repositories.PageInfo.EndCursor
			cursor = &next
		} else {
			break
		}
	}
	return repos, nil
}

func fetchRepoPRAgg(token, owner, repo string, branches []string, since, until time.Time, maxPerBranch int) (map[string]*agg, error) {
	const prQuery = `
query($owner:String!, $name:String!, $base:String!, $cursor:String) {
  repository(owner:$owner, name:$name) {
    pullRequests(
      first: 100
      after: $cursor
      states: MERGED
      orderBy: { field: UPDATED_AT, direction: DESC }
      baseRefName: $base
    ) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number
        mergedAt
        additions
        deletions
        baseRefName
        author { login }
      }
    }
  }
}`
	totals := map[string]*agg{}
	for _, base := range branches {
		var cursor *string
		scanned := 0
		for {
			vars := map[string]interface{}{
				"owner": owner,
				"name":  repo,
				"base":  base,
				"cursor": func() interface{} {
					if cursor == nil {
						return nil
					}
					return *cursor
				}(),
			}
			b, err := doGraphQL(token, prQuery, vars)
			if err != nil {
				return nil, fmt.Errorf("repo %s/%s base %s: %w", owner, repo, base, err)
			}
			var out prResp
			if err := json.Unmarshal(b, &out); err != nil {
				return nil, err
			}
			if len(out.Errors) > 0 {
				msgs := make([]string, 0, len(out.Errors))
				for _, e := range out.Errors {
					msgs = append(msgs, e.Message)
				}
				return nil, errors.New(strings.Join(msgs, "; "))
			}

			nodes := out.Data.Repository.PullRequests.Nodes
			if len(nodes) == 0 {
				break
			}
			for _, n := range nodes {
				scanned++
				if inRange(n.MergedAt, since, until) {
					login := n.Author.Login
					if login == "" {
						login = "(unknown)"
					}
					a := totals[login]
					if a == nil {
						a = &agg{}
						totals[login] = a
					}
					a.Additions += n.Additions
					a.Deletions += n.Deletions
					a.PRs += 1
				}
				if scanned >= maxPerBranch {
					break
				}
			}
			if scanned >= maxPerBranch {
				break
			}
			if out.Data.Repository.PullRequests.PageInfo.HasNextPage {
				next := out.Data.Repository.PullRequests.PageInfo.EndCursor
				cursor = &next
			} else {
				break
			}
		}
	}
	return totals, nil
}

func main() {
	var (
		org             = flag.String("org", "", "GitHub organization login (required)")
		branchesRE      = flag.String("branches", "^(master|main|develop|staging|testing)$", "Regex of base branches to include")
		sinceStr        = flag.String("since", "", "Include PRs merged at or after this time (RFC3339 or 2006-01-02)")
		untilStr        = flag.String("until", "", "Include PRs merged at or before this time (RFC3339 or 2006-01-02)")
		includeForks    = flag.Bool("include-forks", false, "Include forked repositories")
		includeArchived = flag.Bool("include-archived", false, "Include archived repositories")
		visibility      = flag.String("visibility", "all", "Repository visibility: all|public|private (mapped to privacy)")
		maxRepos        = flag.Int("max-repos", 0, "Safety cap: stop after scanning N repos (0 = no cap)")
		maxPerBr        = flag.Int("max-per-branch", 1000, "Safety cap: max PRs to scan per branch per repo")
		out             = flag.String("out", "", "Write CSV to file (default stdout)")
	)
	flag.Parse()

	if *org == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --org is required")
		os.Exit(1)
	}

	token := os.Getenv("GITHUB_ACCESS_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "ERROR: set GITHUB_ACCESS_TOKEN env var with a PAT that can read the org repos")
		os.Exit(1)
	}

	re := regexp.MustCompile(*branchesRE)
	// よく使うブランチ名から正規表現で抽出（必要なら拡張）
	candidates := []string{"master", "main", "develop", "staging", "testing"}
	var branches []string
	for _, b := range candidates {
		if re.MatchString(b) {
			branches = append(branches, b)
		}
	}
	if len(branches) == 0 {
		fmt.Fprintln(os.Stderr, "WARN: no branches match regex; nothing to do")
		return
	}

	since := mustParseTimeOrZero(*sinceStr)
	until := mustParseTimeOrZero(*untilStr)

	// 1) org内の全repo取得
	repos, err := fetchOrgRepos(token, *org, *includeForks, *includeArchived, *visibility, *maxRepos)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR fetching repos: %v\n", err)
		os.Exit(1)
	}
	if len(repos) == 0 {
		fmt.Fprintln(os.Stderr, "WARN: no repositories to scan")
		return
	}

	// 2) 各repoでPR集計 → org/author累計
	type row struct {
		Org       string
		Repo      string
		User      string
		Additions int
		Deletions int
		PRs       int
		Score     int
	}

	var rows []row
	orgTotals := map[string]*agg{} // 著者ごとの全repo合算
	for _, repo := range repos {
		perRepo, err := fetchRepoPRAgg(token, *org, repo, branches, since, until, *maxPerBr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR on %s/%s: %v\n", *org, repo, err)
			os.Exit(1)
		}
		for user, a := range perRepo {
			rows = append(rows, row{
				Org:       *org,
				Repo:      repo,
				User:      user,
				Additions: a.Additions,
				Deletions: a.Deletions,
				PRs:       a.PRs,
				Score:     a.Additions + abs(a.Deletions),
			})
			t := orgTotals[user]
			if t == nil {
				t = &agg{}
				orgTotals[user] = t
			}
			t.Additions += a.Additions
			t.Deletions += a.Deletions
			t.PRs += a.PRs
		}
	}

	// 並びは touched lines 降順（additions + |deletions|）
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Score == rows[j].Score {
			if rows[i].User == rows[j].User {
				if rows[i].Org == rows[j].Org {
					return rows[i].Repo < rows[j].Repo
				}
				return rows[i].Org < rows[j].Org
			}
			return rows[i].User < rows[j].User
		}
		return rows[i].Score > rows[j].Score
	})

	// 出力
	var w io.Writer = os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR creating %s: %v\n", *out, err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"org", "repo", "user", "additions", "deletions", "prs"})
	for _, r := range rows {
		_ = cw.Write([]string{
			r.Org, r.Repo, r.User,
			fmt.Sprintf("%d", r.Additions),
			fmt.Sprintf("%d", r.Deletions),
			fmt.Sprintf("%d", r.PRs),
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR writing csv: %v\n", err)
		os.Exit(1)
	}

	// 参考: 組織合算を最後にstderrで軽く要約
	type sumRow struct {
		User      string
		Additions int
		Deletions int
		PRs       int
		Score     int
	}
	var sumRows []sumRow
	for user, a := range orgTotals {
		sumRows = append(sumRows, sumRow{
			User:      user,
			Additions: a.Additions,
			Deletions: a.Deletions,
			PRs:       a.PRs,
			Score:     a.Additions + abs(a.Deletions),
		})
	}
	sort.Slice(sumRows, func(i, j int) bool {
		if sumRows[i].Score == sumRows[j].Score {
			return sumRows[i].User < sumRows[j].User
		}
		return sumRows[i].Score > sumRows[j].Score
	})
	fmt.Fprintf(os.Stderr, "Scanned %d repos. Top contributors (org total):\n", len(repos))
	for i := 0; i < len(sumRows) && i < 10; i++ {
		s := sumRows[i]
		fmt.Fprintf(os.Stderr, "  %d) %-20s  +%d / -%d  PRs:%d\n", i+1, s.User, s.Additions, s.Deletions, s.PRs)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
