// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate
//counterfeiter:generate -o ../../mocks/pr_commenter.go --fake-name PRCommenter . PRCommenter

package prcomment

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/errors"
)

const githubAPIBaseURL = "https://api.github.com"

// PRCommenter posts a plain COMMENT review on a GitHub pull request. It never
// approves or requests changes — the controller never gates the PR merge.
// PostComment is best-effort: it returns an error only for genuine send
// failures; callers swallow the error and log at WARNING (the frontmatter
// escalation must not be blocked by a failed comment).
type PRCommenter interface {
	// PostComment posts body as a COMMENT review on the PR identified by
	// frontmatter (pr_url, or repository + pull_request_number). Returns an
	// error when the PR cannot be resolved, the token is missing, or the
	// GitHub API call fails. The error carries a frozen substring so callers
	// log the right WARNING.
	PostComment(ctx context.Context, frontmatter lib.TaskFrontmatter, body string) error
}

// NewPRCommenter constructs a PRCommenter. token may be empty — in that case
// PostComment returns an error with the missing-token substring and posts nothing.
func NewPRCommenter(httpClient *http.Client, baseURL, token string) PRCommenter {
	return &prCommenter{
		httpClient: httpClient,
		baseURL:   baseURL,
		token:     token,
	}
}

type prCommenter struct {
	httpClient *http.Client
	baseURL   string
	token     string
}

var prURLRE = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/pull/(\d+)$`)
var repoSlugRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type reviewRequest struct {
	Event string `json:"event"`
	Body  string `json:"body"`
}

func (c *prCommenter) PostComment(ctx context.Context, frontmatter lib.TaskFrontmatter, body string) error {
	if c.token == "" {
		return errors.Errorf(ctx, "planning-retry: github token missing; skipping COMMENT post")
	}

	owner, repo, number, err := c.resolvePR(ctx, frontmatter)
	if err != nil {
		return err
	}

	url := c.baseURL + "/repos/" + owner + "/" + repo + "/pulls/" + strconv.Itoa(number) + "/reviews"
	reqBody := reviewRequest{
		Event: "COMMENT",
		Body:  body,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal review request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return errors.Wrapf(ctx, err, "planning-retry: github COMMENT post failed")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.Wrapf(ctx, err, "planning-retry: github COMMENT post failed")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.Errorf(ctx, "planning-retry: github COMMENT post failed: status %d", resp.StatusCode)
	}
	return nil
}

func (c *prCommenter) resolvePR(ctx context.Context, frontmatter lib.TaskFrontmatter) (owner, repo string, number int, err error) {
	if prURL, ok := frontmatter.String("pr_url"); ok && prURL != "" {
		matches := prURLRE.FindStringSubmatch(prURL)
		if len(matches) == 4 {
			prNumber, parseErr := strconv.Atoi(matches[3])
			if parseErr != nil {
				return "", "", 0, errors.Errorf(ctx, "planning-retry: cannot resolve PR from task: invalid pr_url number")
			}
			return matches[1], matches[2], prNumber, nil
		}
	}

	repoSlug, _ := frontmatter.String("repository")
	rawNumber, hasNumber := frontmatter.Int("pull_request_number")
	if repoSlug == "" || !hasNumber {
		return "", "", 0, errors.Errorf(ctx, "planning-retry: cannot resolve PR from task: no pr_url or repository/pull_request_number in frontmatter")
	}
	if !repoSlugRE.MatchString(repoSlug) {
		return "", "", 0, errors.Errorf(ctx, "planning-retry: cannot resolve PR from task: invalid repository slug")
	}
	if rawNumber <= 0 {
		return "", "", 0, errors.Errorf(ctx, "planning-retry: cannot resolve PR from task: invalid pull_request_number")
	}
	parts := strings.Split(repoSlug, "/")
	return parts[0], parts[1], rawNumber, nil
}
