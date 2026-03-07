package tracker

import "time"

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
