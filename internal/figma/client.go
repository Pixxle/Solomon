package figma

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const baseURL = "https://api.figma.com"

type Client struct {
	token  string
	scale  int
	format string
	client *http.Client
}

func NewClient(token string, scale int, format string) *Client {
	return &Client{
		token:  token,
		scale:  scale,
		format: format,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) Validate(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/v1/me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Figma-Token", c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to Figma: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Figma auth failed (status %d)", resp.StatusCode)
	}
	return nil
}

type FileMetadata struct {
	Name         string `json:"name"`
	LastModified string `json:"lastModified"`
}

func (c *Client) GetFile(ctx context.Context, fileKey string) (*FileMetadata, error) {
	req, err := c.newRequest(ctx, fmt.Sprintf("/v1/files/%s?depth=1", fileKey))
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result FileMetadata
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

type ExportResult struct {
	NodeID   string
	ImageURL string
	Data     []byte
}

func (c *Client) ExportNodes(ctx context.Context, fileKey string, nodeIDs []string) ([]ExportResult, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}

	idsParam := ""
	for i, id := range nodeIDs {
		if i > 0 {
			idsParam += ","
		}
		idsParam += id
	}

	req, err := c.newRequest(ctx, fmt.Sprintf("/v1/images/%s?ids=%s&scale=%d&format=%s",
		fileKey, idsParam, c.scale, c.format))
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Images map[string]string `json:"images"`
		Err    string            `json:"err"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Err != "" {
		return nil, fmt.Errorf("Figma export error: %s", result.Err)
	}

	var exports []ExportResult
	for nodeID, imageURL := range result.Images {
		if imageURL == "" {
			continue
		}
		data, err := c.downloadImage(ctx, imageURL)
		if err != nil {
			return nil, fmt.Errorf("downloading Figma export for node %s: %w", nodeID, err)
		}
		exports = append(exports, ExportResult{
			NodeID:   nodeID,
			ImageURL: imageURL,
			Data:     data,
		})
	}
	return exports, nil
}

func (c *Client) downloadImage(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) newRequest(ctx context.Context, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Figma-Token", c.token)
	return req, nil
}
