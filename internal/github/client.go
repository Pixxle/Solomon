package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type PRComment struct {
	ID        int64  `json:"id"`
	NodeID    string `json:"nodeId"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	Type      string `json:"type"`     // "review_comment" or "issue_comment"
	Reaction  string `json:"reaction"` // "thumbs_up" or "eyes"
}

type Client struct {
	repoPath string
}

func NewClient(repoPath string) *Client {
	return &Client{repoPath: repoPath}
}

// GitIdentity holds the authenticated GitHub user's identity for git commits.
type GitIdentity struct {
	Login string // GitHub username (e.g. "my-bot")
	Name  string // Display name (e.g. "My Bot")
	Email string // Commit email (noreply address or primary email)
}

// GetAuthenticatedIdentity fetches the full git identity (login, name, email) for the
// authenticated GitHub user. Uses the GitHub noreply email for privacy.
func (c *Client) GetAuthenticatedIdentity(ctx context.Context) (*GitIdentity, error) {
	out, err := c.gh(ctx, "api", "user", "--jq", `[.login, .name // .login, .id] | @tsv`)
	if err != nil {
		return nil, fmt.Errorf("fetching user identity: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(out), "\t")
	if len(parts) < 3 {
		return nil, fmt.Errorf("unexpected user API response: %s", out)
	}
	login := parts[0]
	name := parts[1]
	// Use GitHub's noreply email: <id>+<login>@users.noreply.github.com
	email := parts[2] + "+" + login + "@users.noreply.github.com"
	return &GitIdentity{Login: login, Name: name, Email: email}, nil
}

func (c *Client) FindPRForBranch(ctx context.Context, branch string) (int, error) {
	// Check open PRs
	out, err := c.gh(ctx, "pr", "list", "--head", branch, "--state", "open", "--json", "number", "--limit", "1")
	if err == nil {
		if n := parsePRNumber(out); n > 0 {
			return n, nil
		}
	}
	// Check merged PRs
	out, err = c.gh(ctx, "pr", "list", "--head", branch, "--state", "merged", "--json", "number", "--limit", "1")
	if err == nil {
		if n := parsePRNumber(out); n > 0 {
			return n, nil
		}
	}
	return 0, nil
}

func (c *Client) GetPRComments(ctx context.Context, prNumber int, since *string) ([]PRComment, error) {
	prStr := strconv.Itoa(prNumber)

	// Get review comments
	reviewOut, _ := c.gh(ctx, "api", fmt.Sprintf("repos/{owner}/{repo}/pulls/%s/comments", prStr))
	// Get issue comments
	issueOut, _ := c.gh(ctx, "api", fmt.Sprintf("repos/{owner}/{repo}/issues/%s/comments", prStr))

	var comments []PRComment
	comments = append(comments, parseComments(reviewOut, "review_comment")...)
	comments = append(comments, parseComments(issueOut, "issue_comment")...)

	if since != nil {
		var filtered []PRComment
		for _, c := range comments {
			if c.CreatedAt > *since {
				filtered = append(filtered, c)
			}
		}
		comments = filtered
	}

	return comments, nil
}

func (c *Client) GetPRCheckStatus(ctx context.Context, prNumber int) (string, error) {
	out, err := c.gh(ctx, "pr", "checks", strconv.Itoa(prNumber), "--json", "state", "--jq", ".[].state")
	if err != nil {
		return "pending", nil
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return "pending", nil
	}

	for _, line := range lines {
		state := strings.TrimSpace(strings.ToUpper(line))
		if state == "FAILURE" || state == "ERROR" {
			return "failure", nil
		}
	}

	for _, line := range lines {
		state := strings.TrimSpace(strings.ToUpper(line))
		if state != "SUCCESS" && state != "SKIPPED" && state != "" {
			return "pending", nil
		}
	}

	return "success", nil
}

func (c *Client) IsPRMerged(ctx context.Context, prNumber int) (bool, error) {
	out, err := c.gh(ctx, "pr", "view", strconv.Itoa(prNumber), "--json", "mergedAt", "--jq", ".mergedAt")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "" && strings.TrimSpace(out) != "null", nil
}

func (c *Client) CreatePR(ctx context.Context, title, body, branch string, draft bool) (int, error) {
	args := []string{"pr", "create", "--title", title, "--body", body, "--head", branch}
	if draft {
		args = append(args, "--draft")
	}
	out, err := c.gh(ctx, args...)
	if err != nil {
		return 0, fmt.Errorf("creating PR: %w", err)
	}
	// Extract PR number from URL output
	parts := strings.Split(strings.TrimSpace(out), "/")
	if len(parts) > 0 {
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("could not parse PR number from: %s", out)
}

func (c *Client) PostPRComment(ctx context.Context, prNumber int, body string) error {
	cmd := exec.CommandContext(ctx, "gh", "pr", "comment", strconv.Itoa(prNumber), "--body-file", "-")
	cmd.Dir = c.repoPath
	cmd.Stdin = strings.NewReader(body)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("posting comment: %w: %s", err, string(out))
	}
	return nil
}

func (c *Client) PushBranch(ctx context.Context, branch, cwd string) error {
	// Pull with rebase first
	cmd := exec.CommandContext(ctx, "git", "pull", "--rebase", "origin", branch)
	cmd.Dir = cwd
	_ = cmd.Run() // Ignore errors (branch may not exist on remote yet)

	pushCmd := exec.CommandContext(ctx, "git", "push", "--set-upstream", "origin", branch)
	pushCmd.Dir = cwd
	out, err := pushCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pushing branch: %w: %s", err, string(out))
	}
	return nil
}

// ResolveGitHubUsername attempts to map a tracker display name or email to a GitHub username
// by searching GitHub users. Returns empty string if no match found.
func (c *Client) ResolveGitHubUsername(ctx context.Context, nameOrEmail string) string {
	// Try searching by email first
	if strings.Contains(nameOrEmail, "@") {
		out, err := c.gh(ctx, "api", "search/users", "-f", "q="+nameOrEmail+" in:email", "--jq", ".items[0].login")
		if err == nil {
			login := strings.TrimSpace(out)
			if login != "" && login != "null" {
				return login
			}
		}
	}
	// Try searching by name
	out, err := c.gh(ctx, "api", "search/users", "-f", "q="+nameOrEmail+" in:name", "--jq", ".items[0].login")
	if err == nil {
		login := strings.TrimSpace(out)
		if login != "" && login != "null" {
			return login
		}
	}
	return ""
}

func (c *Client) AddReviewers(ctx context.Context, prNumber int, usernames []string) error {
	if len(usernames) == 0 {
		return nil
	}
	args := []string{"pr", "edit", strconv.Itoa(prNumber)}
	for _, u := range usernames {
		args = append(args, "--add-reviewer", u)
	}
	_, err := c.gh(ctx, args...)
	return err
}

func (c *Client) MarkPRReady(ctx context.Context, prNumber int) error {
	_, err := c.gh(ctx, "pr", "ready", strconv.Itoa(prNumber))
	return err
}

func (c *Client) GetCIFailureLogs(ctx context.Context, prNumber int) (string, error) {
	// Get the run ID for the PR's head SHA
	out, err := c.gh(ctx, "pr", "checks", strconv.Itoa(prNumber), "--json", "link,state,name", "--jq",
		`.[] | select(.state == "FAILURE" or .state == "ERROR") | .name`)
	if err != nil {
		return "", err
	}

	failedChecks := strings.TrimSpace(out)
	if failedChecks == "" {
		return "", nil
	}

	// Get failed run logs
	logOut, err := c.gh(ctx, "run", "view", "--log-failed")
	if err != nil {
		return "Failed checks: " + failedChecks, nil
	}
	return logOut, nil
}

func (c *Client) gh(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = c.repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

func (c *Client) ghWithStdin(ctx context.Context, input string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = c.repoPath
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, string(out))
	}
	return out, nil
}

func (c *Client) ghGraphQL(ctx context.Context, query string) ([]byte, error) {
	payload, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, fmt.Errorf("marshalling graphql query: %w", err)
	}
	return c.ghWithStdin(ctx, string(payload), "api", "graphql", "--input", "-")
}

func parsePRNumber(jsonOutput string) int {
	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal([]byte(jsonOutput), &prs); err == nil && len(prs) > 0 {
		return prs[0].Number
	}
	return 0
}

func parseComments(jsonData string, commentType string) []PRComment {
	if jsonData == "" {
		return nil
	}

	var raw []struct {
		ID        int64  `json:"id"`
		NodeID    string `json:"node_id"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Reactions struct {
			ThumbsUp int `json:"+1"`
			Eyes     int `json:"eyes"`
		} `json:"reactions"`
	}

	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		return nil
	}

	var comments []PRComment
	for _, r := range raw {
		var reaction string
		if r.Reactions.ThumbsUp > 0 {
			reaction = "thumbs_up"
		} else if r.Reactions.Eyes > 0 {
			reaction = "eyes"
		}
		comments = append(comments, PRComment{
			ID:        r.ID,
			NodeID:    r.NodeID,
			Author:    r.User.Login,
			Body:      r.Body,
			CreatedAt: r.CreatedAt,
			Type:      commentType,
			Reaction:  reaction,
		})
	}
	return comments
}

// ReplyToReviewComment posts an inline reply to a pull request review comment.
func (c *Client) ReplyToReviewComment(ctx context.Context, prNumber int, commentID int64, body string) error {
	payload, err := json.Marshal(map[string]interface{}{
		"body":        body,
		"in_reply_to": commentID,
	})
	if err != nil {
		return fmt.Errorf("marshalling reply payload: %w", err)
	}

	_, err = c.ghWithStdin(ctx, string(payload), "api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/comments", prNumber),
		"--method", "POST", "--input", "-")
	return err
}

// ResolveReviewThread resolves the review thread containing the given comment node ID.
func (c *Client) ResolveReviewThread(ctx context.Context, commentNodeID string) error {
	// PullRequestReviewComment has no direct thread field; traverse via pullRequest.reviewThreads.
	threadQuery := fmt.Sprintf(`query {
		node(id: %q) {
			... on PullRequestReviewComment {
				pullRequest {
					reviewThreads(first: 100) {
						nodes {
							id
							comments(first: 100) {
								nodes { id }
							}
						}
					}
				}
			}
		}
	}`, commentNodeID)
	out, err := c.ghGraphQL(ctx, threadQuery)
	if err != nil {
		return fmt.Errorf("querying review threads: %w", err)
	}

	var result struct {
		Data struct {
			Node struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID       string `json:"id"`
							Comments struct {
								Nodes []struct {
									ID string `json:"id"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return fmt.Errorf("parsing thread query response: %w", err)
	}

	// Find the thread containing our comment node ID.
	var threadID string
outer:
	for _, thread := range result.Data.Node.PullRequest.ReviewThreads.Nodes {
		for _, comment := range thread.Comments.Nodes {
			if comment.ID == commentNodeID {
				threadID = thread.ID
				break outer
			}
		}
	}
	if threadID == "" {
		return fmt.Errorf("could not find review thread for comment %s", commentNodeID)
	}

	resolveMutation := fmt.Sprintf(`mutation { resolveReviewThread(input: {threadId: %q}) { thread { isResolved } } }`, threadID)
	_, err = c.ghGraphQL(ctx, resolveMutation)
	return err
}

// GetPRDiff returns the diff for a given PR number.
func (c *Client) GetPRDiff(ctx context.Context, prNumber int) (string, error) {
	return c.gh(ctx, "pr", "diff", strconv.Itoa(prNumber))
}
