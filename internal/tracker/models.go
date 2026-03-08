package tracker

import (
	"fmt"
	"strings"
	"time"
)

type Issue struct {
	Key         string
	Title       string
	Description string
	Status      string
	Labels      []string
	Created     time.Time
	Updated     time.Time
}

type Comment struct {
	ID      string
	Author  string
	Body    string
	Created time.Time
}

type Reaction struct {
	UserID   string
	Username string
	Type     string // "thumbs_up", "eyes", etc.
}

type Attachment struct {
	ID       string
	Filename string
	URL      string
	MimeType string
}

type Transition struct {
	ID   string
	Name string
}

// FormatConversation formats comments into a conversation string.
// If botUserID is non-empty, each line is prefixed with [human]/[assistant] role.
func FormatConversation(comments []Comment, botUserID string) string {
	var sb strings.Builder
	for _, c := range comments {
		if botUserID != "" {
			role := "human"
			if c.Author == botUserID {
				role = "assistant"
			}
			fmt.Fprintf(&sb, "[%s] %s:\n%s\n\n", role, c.Author, c.Body)
		} else {
			fmt.Fprintf(&sb, "[%s]:\n%s\n\n", c.Author, c.Body)
		}
	}
	return sb.String()
}
