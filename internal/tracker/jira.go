package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pixxle/codehephaestus/internal/config"
)

type JiraTracker struct {
	baseURL  string
	email    string
	apiKey   string
	project  string
	label    string
	client   *http.Client
	statuses map[string]string // friendly name -> Jira status name
}

func NewJiraTracker(cfg *config.Config) (*JiraTracker, error) {
	return &JiraTracker{
		baseURL: strings.TrimRight(cfg.TrackerBaseURL, "/"),
		email:   cfg.TrackerEmail,
		apiKey:  cfg.TrackerAPIKey,
		project: cfg.TrackerProject,
		label:   cfg.TrackerLabel,
		client:  &http.Client{Timeout: 30 * time.Second},
		statuses: map[string]string{
			"todo":        cfg.JiraStatusTodo,
			"in_progress": cfg.JiraStatusInProgress,
			"in_review":   cfg.JiraStatusInReview,
			"done":        cfg.JiraStatusDone,
		},
	}, nil
}

func (j *JiraTracker) ValidateConnection(ctx context.Context) error {
	req, err := j.newRequest(ctx, "GET", "/rest/api/3/myself", nil)
	if err != nil {
		return err
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to Jira: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Jira auth failed (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

func (j *JiraTracker) ResolveCurrentUser(ctx context.Context) (string, error) {
	req, err := j.newRequest(ctx, "GET", "/rest/api/3/myself", nil)
	if err != nil {
		return "", err
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var user struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", err
	}
	return user.AccountID, nil
}

func (j *JiraTracker) FetchIssuesByStatus(ctx context.Context, status string) ([]Issue, error) {
	jql := fmt.Sprintf(`project = %s AND status = "%s" AND labels = "%s" ORDER BY created ASC`,
		j.project, status, j.label)

	req, err := j.newRequest(ctx, "GET",
		fmt.Sprintf("/rest/api/3/search?jql=%s&fields=summary,description,status,labels,created,updated", url.QueryEscape(jql)),
		nil)
	if err != nil {
		return nil, err
	}

	resp, err := j.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Jira search failed (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary     string `json:"summary"`
				Description json.RawMessage `json:"description"`
				Status      struct {
					Name string `json:"name"`
				} `json:"status"`
				Labels  []string `json:"labels"`
				Created string   `json:"created"`
				Updated string   `json:"updated"`
			} `json:"fields"`
		} `json:"issues"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var issues []Issue
	for _, i := range result.Issues {
		desc := extractADFText(i.Fields.Description)
		issues = append(issues, Issue{
			Key:         i.Key,
			Title:       i.Fields.Summary,
			Description: desc,
			Status:      i.Fields.Status.Name,
			Labels:      i.Fields.Labels,
			Created:     parseJiraTime(i.Fields.Created),
			Updated:     parseJiraTime(i.Fields.Updated),
		})
	}
	return issues, nil
}

func (j *JiraTracker) TransitionIssue(ctx context.Context, issueKey string, toStatus string) error {
	// Get available transitions
	req, err := j.newRequest(ctx, "GET", fmt.Sprintf("/rest/api/3/issue/%s/transitions", issueKey), nil)
	if err != nil {
		return err
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var transitions struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&transitions); err != nil {
		return err
	}

	for _, t := range transitions.Transitions {
		if strings.EqualFold(t.To.Name, toStatus) || strings.EqualFold(t.Name, toStatus) {
			body := map[string]interface{}{
				"transition": map[string]string{"id": t.ID},
			}
			b, _ := json.Marshal(body)
			req, err := j.newRequest(ctx, "POST", fmt.Sprintf("/rest/api/3/issue/%s/transitions", issueKey), bytes.NewReader(b))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := j.client.Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				return fmt.Errorf("transition failed (status %d)", resp.StatusCode)
			}
			return nil
		}
	}
	return fmt.Errorf("no transition found to status %q for issue %s", toStatus, issueKey)
}

func (j *JiraTracker) GetIssueBranchName(issue Issue, botSlug string) string {
	slug := slugify(issue.Title)
	return fmt.Sprintf("%s/%s-%s", botSlug, issue.Key, slug)
}

func (j *JiraTracker) GetComments(ctx context.Context, issueKey string) ([]Comment, error) {
	req, err := j.newRequest(ctx, "GET", fmt.Sprintf("/rest/api/3/issue/%s/comment?orderBy=created", issueKey), nil)
	if err != nil {
		return nil, err
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Comments []struct {
			ID     string `json:"id"`
			Author struct {
				AccountID   string `json:"accountId"`
				DisplayName string `json:"displayName"`
			} `json:"author"`
			Body    json.RawMessage `json:"body"`
			Created string          `json:"created"`
		} `json:"comments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var comments []Comment
	for _, c := range result.Comments {
		comments = append(comments, Comment{
			ID:      c.ID,
			Author:  c.Author.AccountID,
			Body:    extractADFText(c.Body),
			Created: parseJiraTime(c.Created),
		})
	}
	return comments, nil
}

func (j *JiraTracker) GetCommentsSince(ctx context.Context, issueKey string, since time.Time) ([]Comment, error) {
	all, err := j.GetComments(ctx, issueKey)
	if err != nil {
		return nil, err
	}
	var filtered []Comment
	for _, c := range all {
		if c.Created.After(since) {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

func (j *JiraTracker) AddComment(ctx context.Context, issueKey string, body string) error {
	adf := textToADF(body)
	b, _ := json.Marshal(map[string]interface{}{"body": adf})
	req, err := j.newRequest(ctx, "POST", fmt.Sprintf("/rest/api/3/issue/%s/comment", issueKey), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("adding comment failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (j *JiraTracker) AttachFile(ctx context.Context, issueKey string, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	writer.Close()

	req, err := j.newRequest(ctx, "POST", fmt.Sprintf("/rest/api/3/issue/%s/attachments", issueKey), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Atlassian-Token", "no-check")

	resp, err := j.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("attaching file failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (j *JiraTracker) GetCommentReactions(ctx context.Context, issueKey string, commentID string) ([]Reaction, error) {
	// Jira doesn't have comment reactions in the same way as GitHub.
	// This is a placeholder - reactions on Jira comments aren't standard.
	return nil, nil
}

func (j *JiraTracker) UpdateDescription(ctx context.Context, issueKey string, description string, attachments []Attachment) error {
	adf := textToADF(description)
	body := map[string]interface{}{
		"fields": map[string]interface{}{
			"description": adf,
		},
	}
	b, _ := json.Marshal(body)
	req, err := j.newRequest(ctx, "PUT", fmt.Sprintf("/rest/api/3/issue/%s", issueKey), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("updating description failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (j *JiraTracker) GetAttachments(ctx context.Context, issueKey string) ([]Attachment, error) {
	req, err := j.newRequest(ctx, "GET", fmt.Sprintf("/rest/api/3/issue/%s?fields=attachment", issueKey), nil)
	if err != nil {
		return nil, err
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Fields struct {
			Attachment []struct {
				ID       string `json:"id"`
				Filename string `json:"filename"`
				Content  string `json:"content"`
				MimeType string `json:"mimeType"`
			} `json:"attachment"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var attachments []Attachment
	for _, a := range result.Fields.Attachment {
		attachments = append(attachments, Attachment{
			ID:       a.ID,
			Filename: a.Filename,
			URL:      a.Content,
			MimeType: a.MimeType,
		})
	}
	return attachments, nil
}

func (j *JiraTracker) DownloadAttachment(ctx context.Context, url string) ([]byte, string, error) {
	req, err := j.newRequest(ctx, "GET", "", nil)
	if err != nil {
		return nil, "", err
	}
	// Override URL for attachment download (absolute URL)
	req.URL, _ = req.URL.Parse(url)

	resp, err := j.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func (j *JiraTracker) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u := j.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(j.email, j.apiKey)
	return req, nil
}

// extractADFText extracts plain text from Atlassian Document Format JSON.
func extractADFText(raw json.RawMessage) string {
	if raw == nil || string(raw) == "null" {
		return ""
	}

	var doc struct {
		Content []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Might be a plain string
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return string(raw)
	}

	var parts []string
	for _, block := range doc.Content {
		var lineParts []string
		for _, inline := range block.Content {
			if inline.Text != "" {
				lineParts = append(lineParts, inline.Text)
			}
		}
		if len(lineParts) > 0 {
			parts = append(parts, strings.Join(lineParts, ""))
		}
	}
	return strings.Join(parts, "\n")
}

// textToADF converts plain text/markdown to minimal Atlassian Document Format.
func textToADF(text string) map[string]interface{} {
	lines := strings.Split(text, "\n")
	var content []interface{}
	for _, line := range lines {
		content = append(content, map[string]interface{}{
			"type": "paragraph",
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": line,
				},
			},
		})
	}
	return map[string]interface{}{
		"version": 1,
		"type":    "doc",
		"content": content,
	}
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
		s = strings.TrimRight(s, "-")
	}
	return s
}

func parseJiraTime(s string) time.Time {
	// Jira uses ISO 8601: 2024-01-01T12:00:00.000+0000
	for _, layout := range []string{
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05.000Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
