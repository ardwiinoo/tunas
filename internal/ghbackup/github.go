// Package ghbackup backs the worklog up to a private GitHub repo, one way:
// each month is rendered in the old worklog app's file format and written
// with the Contents API. It never overwrites a file that changed outside
// Tunas (conflict), and it imports the existing files once.
package ghbackup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Errors.
var (
	ErrNotFound = errors.New("file not found in the backup repo")
	ErrConflict = errors.New("sha mismatch (the file changed in the repo)")
)

// StatusError is an unexpected HTTP status from GitHub.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("github returned %d", e.Code) }

// Client is a minimal GitHub Contents API client. The token only ever goes
// into the Authorization header.
type Client struct {
	Base   string // https://api.github.com
	Token  string
	Owner  string
	Repo   string
	Branch string
	Dir    string // sub folder in the repo ("" = root)
	HTTP   *http.Client
}

// NewClient parses "owner/repo".
func NewClient(token, repo, branch, dir string) (*Client, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("GITHUB_BACKUP_REPO must be owner/repo")
	}
	if branch == "" {
		branch = "main"
	}
	return &Client{Base: "https://api.github.com", Token: token, Owner: owner, Repo: name, Branch: branch,
		Dir: strings.Trim(dir, "/"), HTTP: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (c *Client) filePath(name string) string {
	if c.Dir == "" {
		return name
	}
	return path.Join(c.Dir, name)
}

func (c *Client) url(p string) string {
	return fmt.Sprintf("%s/repos/%s/%s/contents/%s", strings.TrimRight(c.Base, "/"),
		url.PathEscape(c.Owner), url.PathEscape(c.Repo), escapePath(p))
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func (c *Client) do(ctx context.Context, method, u string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("github %s: %w", method, ue.Err) // no URL in logs
		}
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return ErrNotFound
	case resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusUnprocessableEntity:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return ErrConflict
	case resp.StatusCode/100 != 2:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return &StatusError{Code: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// File is a file in the repo.
type File struct {
	Name    string
	SHA     string
	Content string
}

// Get reads a file (ErrNotFound if it does not exist).
func (c *Client) Get(ctx context.Context, name string) (File, error) {
	var out struct {
		Name     string `json:"name"`
		SHA      string `json:"sha"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := c.do(ctx, http.MethodGet, c.url(c.filePath(name))+"?ref="+url.QueryEscape(c.Branch), nil, &out); err != nil {
		return File{}, err
	}
	if out.Encoding != "base64" {
		return File{}, fmt.Errorf("github: unexpected encoding %q", out.Encoding)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return File{}, fmt.Errorf("github: %w", err)
	}
	return File{Name: out.Name, SHA: out.SHA, Content: string(raw)}, nil
}

// Put writes a file. sha must be the current blob sha ("" to create);
// a stale sha is ErrConflict. It returns the new sha.
func (c *Client) Put(ctx context.Context, name, content, sha, message string) (string, error) {
	body := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  c.Branch,
	}
	if sha != "" {
		body["sha"] = sha
	}
	var out struct {
		Content struct {
			SHA string `json:"sha"`
		} `json:"content"`
	}
	if err := c.do(ctx, http.MethodPut, c.url(c.filePath(name)), body, &out); err != nil {
		return "", err
	}
	return out.Content.SHA, nil
}

// List returns the file names in the backup folder.
func (c *Client) List(ctx context.Context) ([]string, error) {
	var out []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	u := c.url(c.Dir)
	if c.Dir == "" {
		u = strings.TrimSuffix(u, "/")
	}
	if err := c.do(ctx, http.MethodGet, u+"?ref="+url.QueryEscape(c.Branch), nil, &out); err != nil {
		return nil, err
	}
	var names []string
	for _, f := range out {
		if f.Type == "file" {
			names = append(names, f.Name)
		}
	}
	return names, nil
}
