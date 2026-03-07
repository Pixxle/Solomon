package figma

import (
	"net/url"
	"regexp"
	"strings"
)

var figmaURLPattern = regexp.MustCompile(`https://www\.figma\.com/(design|file)/([a-zA-Z0-9]+)`)

type ParsedURL struct {
	FileKey string
	NodeIDs []string
}

// ExtractFigmaURLs finds all Figma URLs in text and returns parsed file keys and node IDs.
func ExtractFigmaURLs(text string) []ParsedURL {
	matches := figmaURLPattern.FindAllString(text, -1)
	seen := make(map[string]bool)
	var results []ParsedURL

	for _, match := range matches {
		parsed, err := url.Parse(match)
		if err != nil {
			continue
		}

		parts := strings.Split(parsed.Path, "/")
		if len(parts) < 3 {
			continue
		}
		fileKey := parts[2]
		if seen[fileKey] {
			continue
		}
		seen[fileKey] = true

		var nodeIDs []string
		if nodeParam := parsed.Query().Get("node-id"); nodeParam != "" {
			nodeIDs = strings.Split(nodeParam, ",")
		}

		results = append(results, ParsedURL{
			FileKey: fileKey,
			NodeIDs: nodeIDs,
		})
	}
	return results
}
