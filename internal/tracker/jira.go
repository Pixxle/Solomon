package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pixxle/solomon/internal/config"
)

type JiraTracker struct {
	baseURL       string
	email         string
	apiKey        string
	project       string
	label         string
	approvalLabel string
	client        *http.Client
	statuses      map[string]string // friendly name -> Jira status name
}

func NewJiraTracker(cfg *config.Config) (*JiraTracker, error) {
	return &JiraTracker{
		baseURL:       strings.TrimRight(cfg.TrackerBaseURL, "/"),
		email:         cfg.JiraEmail,
		apiKey:        cfg.TrackerAPIKey,
		project:       cfg.TrackerProject,
		label:         cfg.JiraPlanningLabel,
		approvalLabel: cfg.JiraApprovalLabel,
		client:        &http.Client{Timeout: 30 * time.Second},
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
	jql := fmt.Sprintf(`project = %s AND status = "%s" AND (labels = "%s" OR assignee = currentUser()) ORDER BY created ASC`,
		j.project, status, j.label)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"jql":    jql,
		"fields": []string{"summary", "description", "status", "labels", "assignee", "created", "updated"},
	})

	req, err := j.newRequest(ctx, "POST", "/rest/api/3/search/jql", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

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
				Summary     string          `json:"summary"`
				Description json.RawMessage `json:"description"`
				Status      struct {
					Name string `json:"name"`
				} `json:"status"`
				Labels   []string `json:"labels"`
				Assignee *struct {
					AccountID string `json:"accountId"`
				} `json:"assignee"`
				Created string `json:"created"`
				Updated string `json:"updated"`
			} `json:"fields"`
		} `json:"issues"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var issues []Issue
	for _, i := range result.Issues {
		desc := extractADFText(i.Fields.Description)
		var assignees []string
		if i.Fields.Assignee != nil && i.Fields.Assignee.AccountID != "" {
			assignees = []string{i.Fields.Assignee.AccountID}
		}
		issues = append(issues, Issue{
			Key:         i.Key,
			Title:       i.Fields.Summary,
			Description: desc,
			Status:      i.Fields.Status.Name,
			Labels:      i.Fields.Labels,
			Assignees:   assignees,
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

func (j *JiraTracker) AddComment(ctx context.Context, issueKey string, body string) error {
	_, err := j.AddCommentReturningID(ctx, issueKey, body)
	return err
}

func (j *JiraTracker) AddCommentReturningID(ctx context.Context, issueKey, body string) (string, error) {
	adf := textToADF(body)
	b, _ := json.Marshal(map[string]interface{}{"body": adf})
	req, err := j.newRequest(ctx, "POST", fmt.Sprintf("/rest/api/3/issue/%s/comment", issueKey), bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("adding comment failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding comment response: %w", err)
	}
	return result.ID, nil
}

func (j *JiraTracker) UpdateComment(ctx context.Context, issueKey, commentID, body string) error {
	adf := textToADF(body)
	b, _ := json.Marshal(map[string]interface{}{"body": adf})
	req, err := j.newRequest(ctx, "PUT", fmt.Sprintf("/rest/api/3/issue/%s/comment/%s", issueKey, commentID), bytes.NewReader(b))
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
		return fmt.Errorf("updating comment failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (j *JiraTracker) DeleteComment(ctx context.Context, issueKey, commentID string) error {
	req, err := j.newRequest(ctx, "DELETE", fmt.Sprintf("/rest/api/3/issue/%s/comment/%s", issueKey, commentID), nil)
	if err != nil {
		return err
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("deleting comment failed (status %d): %s", resp.StatusCode, string(respBody))
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
	commentIDInt, err := strconv.ParseInt(commentID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid comment ID %q: %w", commentID, err)
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"commentIds": []int64{commentIDInt},
	})

	req, err := j.newRequest(ctx, "POST", "/jira/rest/internal/2/reactions/view", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := j.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return nil, nil
	}

	var results []struct {
		EmojiID string `json:"emojiId"`
		Count   int    `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decoding reactions response: %w", err)
	}

	var reactions []Reaction
	for _, r := range results {
		if r.Count == 0 {
			continue
		}
		reactionType := r.EmojiID
		if r.EmojiID == "1f44d" {
			reactionType = "thumbs_up"
		}
		reactions = append(reactions, Reaction{Type: reactionType})
	}
	return reactions, nil
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

func (j *JiraTracker) IsReadySignal(_ context.Context, issue Issue, _ string) (bool, error) {
	return issue.HasLabel(j.approvalLabel), nil
}

func (j *JiraTracker) ReadySignalInstruction() string {
	return fmt.Sprintf("add the `%s` label to begin implementation", j.approvalLabel)
}

func (j *JiraTracker) ClearReadySignal(ctx context.Context, issueKey string) error {
	body := map[string]interface{}{
		"update": map[string]interface{}{
			"labels": []map[string]string{
				{"remove": j.approvalLabel},
			},
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
		return fmt.Errorf("removing approval label failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (j *JiraTracker) CreateIssue(ctx context.Context, title, description string, labels []string) (string, error) {
	payload := map[string]interface{}{
		"fields": map[string]interface{}{
			"project":   map[string]string{"key": j.project},
			"summary":   title,
			"issuetype": map[string]string{"name": "Task"},
			"description": textToADF(description),
			"labels":      labels,
		},
	}
	b, _ := json.Marshal(payload)
	req, err := j.newRequest(ctx, "POST", "/rest/api/3/issue", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("creating issue failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	var result struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding create issue response: %w", err)
	}
	return result.Key, nil
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

// textToADF converts markdown-like text to Atlassian Document Format.
func textToADF(text string) map[string]interface{} {
	lines := strings.Split(text, "\n")
	var content []interface{}
	i := 0
	for i < len(lines) {
		line := lines[i]

		// Horizontal rule
		if strings.TrimSpace(line) == "---" {
			content = append(content, adfRule())
			i++
			continue
		}

		// Headings
		if level, heading := parseHeading(line); level > 0 {
			content = append(content, adfHeading(level, heading))
			i++
			continue
		}

		// Table block
		if strings.HasPrefix(strings.TrimSpace(line), "|") && strings.HasSuffix(strings.TrimSpace(line), "|") {
			var tableLines []string
			for i < len(lines) {
				trimmed := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
					break
				}
				tableLines = append(tableLines, lines[i])
				i++
			}
			if node := adfTable(tableLines); node != nil {
				content = append(content, node)
			}
			continue
		}

		// Bullet list / task list block
		if isBulletLine(line) {
			var items []interface{}
			for i < len(lines) && isBulletLine(lines[i]) {
				itemText := stripBullet(lines[i])
				if checked, isTask := parseTaskItem(itemText); isTask {
					items = append(items, adfTaskItem(checked, checkedText(itemText)))
				} else {
					items = append(items, adfListItem(itemText))
				}
				i++
			}
			// Use taskList if all items are tasks, otherwise bulletList
			allTasks := true
			for _, item := range items {
				m := item.(map[string]interface{})
				if m["type"] != "taskItem" {
					allTasks = false
					break
				}
			}
			if allTasks {
				content = append(content, map[string]interface{}{
					"type":    "taskList",
					"attrs":   map[string]interface{}{"localId": ""},
					"content": items,
				})
			} else {
				content = append(content, map[string]interface{}{
					"type":    "bulletList",
					"content": items,
				})
			}
			continue
		}

		// Numbered list block
		if isNumberedLine(line) {
			var items []interface{}
			for i < len(lines) && isNumberedLine(lines[i]) {
				itemText := stripNumbered(lines[i])
				items = append(items, adfListItem(itemText))
				i++
			}
			content = append(content, map[string]interface{}{
				"type":    "orderedList",
				"content": items,
			})
			continue
		}

		// Empty line → skip (don't create empty paragraphs)
		if strings.TrimSpace(line) == "" {
			i++
			continue
		}

		// Regular paragraph
		content = append(content, adfParagraph(line))
		i++
	}

	if len(content) == 0 {
		content = append(content, adfParagraph(""))
	}

	return map[string]interface{}{
		"version": 1,
		"type":    "doc",
		"content": content,
	}
}

func parseHeading(line string) (int, string) {
	trimmed := strings.TrimSpace(line)
	level := 0
	for _, c := range trimmed {
		if c == '#' {
			level++
		} else {
			break
		}
	}
	if level >= 1 && level <= 6 && len(trimmed) > level && trimmed[level] == ' ' {
		return level, strings.TrimSpace(trimmed[level+1:])
	}
	return 0, ""
}

func adfHeading(level int, text string) map[string]interface{} {
	return map[string]interface{}{
		"type":    "heading",
		"attrs":   map[string]interface{}{"level": level},
		"content": adfInlineMarkdown(text),
	}
}

func adfParagraph(text string) map[string]interface{} {
	return map[string]interface{}{
		"type":    "paragraph",
		"content": adfInlineMarkdown(text),
	}
}

func adfRule() map[string]interface{} {
	return map[string]interface{}{"type": "rule"}
}

func adfListItem(text string) map[string]interface{} {
	return map[string]interface{}{
		"type": "listItem",
		"content": []interface{}{
			map[string]interface{}{
				"type":    "paragraph",
				"content": adfInlineMarkdown(text),
			},
		},
	}
}

func adfTaskItem(checked bool, text string) map[string]interface{} {
	state := "TODO"
	if checked {
		state = "DONE"
	}
	return map[string]interface{}{
		"type":    "taskItem",
		"attrs":   map[string]interface{}{"localId": "", "state": state},
		"content": adfInlineMarkdown(text),
	}
}

var (
	bulletRe   = regexp.MustCompile(`^\s*[-*]\s+`)
	numberedRe = regexp.MustCompile(`^\s*\d+\.\s+`)
	taskRe     = regexp.MustCompile(`^\[[ xX]\]\s*`)
)

func isBulletLine(line string) bool   { return bulletRe.MatchString(line) }
func isNumberedLine(line string) bool { return numberedRe.MatchString(line) }

func stripBullet(line string) string {
	return strings.TrimSpace(bulletRe.ReplaceAllString(line, ""))
}

func stripNumbered(line string) string {
	return strings.TrimSpace(numberedRe.ReplaceAllString(line, ""))
}

func parseTaskItem(text string) (checked bool, isTask bool) {
	if strings.HasPrefix(text, "[ ] ") || strings.HasPrefix(text, "[ ]") {
		return false, true
	}
	if strings.HasPrefix(text, "[x] ") || strings.HasPrefix(text, "[X] ") ||
		strings.HasPrefix(text, "[x]") || strings.HasPrefix(text, "[X]") {
		return true, true
	}
	return false, false
}

func checkedText(text string) string {
	return strings.TrimSpace(taskRe.ReplaceAllString(text, ""))
}

// adfInlineMarkdown parses **bold** and `code` within a text string.
func adfInlineMarkdown(text string) []interface{} {
	var nodes []interface{}
	for len(text) > 0 {
		// Bold
		if idx := strings.Index(text, "**"); idx >= 0 {
			end := strings.Index(text[idx+2:], "**")
			if end >= 0 {
				if idx > 0 {
					nodes = append(nodes, adfText(text[:idx], nil))
				}
				nodes = append(nodes, adfText(text[idx+2:idx+2+end], []interface{}{
					map[string]interface{}{"type": "strong"},
				}))
				text = text[idx+2+end+2:]
				continue
			}
		}
		// Inline code
		if idx := strings.Index(text, "`"); idx >= 0 {
			end := strings.Index(text[idx+1:], "`")
			if end >= 0 {
				if idx > 0 {
					nodes = append(nodes, adfText(text[:idx], nil))
				}
				nodes = append(nodes, adfText(text[idx+1:idx+1+end], []interface{}{
					map[string]interface{}{"type": "code"},
				}))
				text = text[idx+1+end+1:]
				continue
			}
		}
		// Plain text remainder
		nodes = append(nodes, adfText(text, nil))
		break
	}
	if len(nodes) == 0 {
		nodes = append(nodes, adfText("", nil))
	}
	return nodes
}

func adfText(text string, marks []interface{}) map[string]interface{} {
	node := map[string]interface{}{
		"type": "text",
		"text": text,
	}
	if len(marks) > 0 {
		node["marks"] = marks
	}
	return node
}

func adfTable(lines []string) map[string]interface{} {
	if len(lines) < 2 {
		return nil
	}
	// Check if second line is separator (|---|---|)
	isSep := func(line string) bool {
		cells := splitTableRow(line)
		for _, c := range cells {
			trimmed := strings.TrimSpace(c)
			if trimmed != "" && strings.Trim(trimmed, "-:") != "" {
				return false
			}
		}
		return true
	}

	hasHeader := len(lines) >= 2 && isSep(lines[1])
	var rows []interface{}

	for i, line := range lines {
		if hasHeader && i == 1 {
			continue // skip separator
		}
		cells := splitTableRow(line)
		isHeader := hasHeader && i == 0
		var cellNodes []interface{}
		for _, cell := range cells {
			cellType := "tableCell"
			if isHeader {
				cellType = "tableHeader"
			}
			cellNodes = append(cellNodes, map[string]interface{}{
				"type": cellType,
				"content": []interface{}{
					adfParagraph(strings.TrimSpace(cell)),
				},
			})
		}
		rows = append(rows, map[string]interface{}{
			"type":    "tableRow",
			"content": cellNodes,
		})
	}

	return map[string]interface{}{
		"type":    "table",
		"attrs":   map[string]interface{}{"isNumberColumnEnabled": false, "layout": "default"},
		"content": rows,
	}
}

func splitTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	return strings.Split(trimmed, "|")
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
