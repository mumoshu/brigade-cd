package webhook

import (
	"context"
	"fmt"
	"io/ioutil"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v27/github"
	"golang.org/x/oauth2"

	"github.com/brigadecore/brigade/pkg/brigade"
)

// State names for GitHub status
var (
	StatePending = "pending"
	StateFailure = "failure"
	StateError   = "error"
	StateSuccess = "success"
)

// StatusContext names the context for a particular status message.
const StatusContext = "brigade"

// GhClient gets a new GitHub client object.
//
// It authenticates with an OAUTH2 token.
//
// If an enterpriseHost base URL is provided, this will attempt to connect to
// that instead of the hosted GitHub API server.
func GhClient(gh brigade.Github) (*github.Client, error) {
	t := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: gh.Token})
	c := context.Background()
	tc := oauth2.NewClient(c, t)
	if gh.BaseURL != "" {
		return github.NewEnterpriseClient(gh.BaseURL, gh.UploadURL, tc)
	}
	return github.NewClient(tc), nil
}

// setRepoStatus sets the status on a particular commit in a repo.
func setRepoStatus(commit string, proj *brigade.Project, status *github.RepoStatus) error {
	if proj.Github.Token == "" {
		return fmt.Errorf("status update skipped because no GitHubToken exists on %s", proj.Name)
	}
	parts := strings.SplitN(proj.Repo.Name, "/", 3)
	if len(parts) != 3 {
		return fmt.Errorf("project name %q is malformed", proj.Repo.Name)
	}
	c := context.Background()
	client, err := GhClient(proj.Github)
	if err != nil {
		return err
	}
	_, _, err = client.Repositories.CreateStatus(
		c,
		parts[1],
		parts[2],
		commit,
		status)
	return err
}

// GetRepoStatus gets the Brigade repository status.
// The ref can be a SHA or a branch or tag.
func GetRepoStatus(proj *brigade.Project, ref string) (*github.RepoStatus, error) {
	c := context.Background()
	client, err := GhClient(proj.Github)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(proj.Repo.Name, "/", 3) // github.com/ORG/REPO
	if len(parts) != 3 {
		return nil, fmt.Errorf("project name %q is malformed", proj.Repo.Name)
	}
	statii, _, err := client.Repositories.ListStatuses(c, parts[1], parts[2], ref, &github.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, status := range statii {
		if *status.Context == StatusContext {
			return status, nil
		}
	}
	return nil, fmt.Errorf("no brigade status found")
}

// GetLastCommit gets the last commit on the give reference (branch name or tag).
func GetLastCommit(proj *brigade.Project, ref string) (string, error) {
	c := context.Background()
	client, err := GhClient(proj.Github)
	if err != nil {
		return "", err
	}
	parts := strings.SplitN(proj.Repo.Name, "/", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("project name %q is malformed", proj.Repo.Name)
	}
	sha, _, err := client.Repositories.GetCommitSHA1(c, parts[1], parts[2], ref, "")
	return sha, err
}

// GetFileContents returns the contents for a particular file in the project.
func GetFileContents(proj *brigade.Project, ref, path string) ([]byte, error) {
	c := context.Background()
	client, err := GhClient(proj.Github)
	if err != nil {
		return []byte{}, err
	}
	parts := strings.SplitN(proj.Repo.Name, "/", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("project name %q is malformed", proj.Repo.Name)
	}
	opts := &github.RepositoryContentGetOptions{Ref: ref}
	r, err := client.Repositories.DownloadContents(c, parts[1], parts[2], path, opts)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return ioutil.ReadAll(r)

}

func (s *githubHook) installationToken(appID, installationID int, cfg brigade.Github) (string, time.Time, error) {
	aidStr := strconv.Itoa(appID)
	// We need to perform auth here, and then inject the token into the
	// body so that the app can use it.
	tok, err := JWT(aidStr, s.key)
	if err != nil {
		return "", time.Time{}, err
	}

	ghc, err := GhClient(brigade.Github{
		Token:     tok,
		BaseURL:   cfg.BaseURL,
		UploadURL: cfg.UploadURL,
	})

	if err != nil {
		return "", time.Time{}, err
	}

	ctx := context.Background()
	itok, _, err := ghc.Apps.CreateInstallationToken(ctx, int64(installationID))
	if err != nil {
		return "", time.Time{}, err
	}
	return itok.GetToken(), itok.GetExpiresAt(), nil
}

// InstallationTokenClient uses an installation token to authenticate to the Github API.
func InstallationTokenClient(instToken, baseURL, uploadURL string) (*github.Client, error) {
	// For installation tokens, Github uses a different token type ("token" instead of "bearer")
	t := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: instToken, TokenType: "token"})
	c := context.Background()
	tc := oauth2.NewClient(c, t)
	if baseURL != "" {
		return github.NewEnterpriseClient(baseURL, uploadURL, tc)
	}
	return github.NewClient(tc), nil
}
