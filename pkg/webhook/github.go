package webhook

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"

	"github.com/google/go-github/v27/github"
	"gopkg.in/gin-gonic/gin.v1"

	"github.com/brigadecore/brigade/pkg/brigade"
	"github.com/brigadecore/brigade/pkg/storage"
)

const hubSignatureHeader = "X-Hub-Signature"

// ErrAuthFailed indicates some part of the auth handshake failed
//
// This is usually indicative of an auth failure between the client library and GitHub
var ErrAuthFailed = errors.New("Auth Failed")

type githubHook struct {
	store                   storage.Store
	getFile                 fileGetter
	createStatus            statusCreator
	handleIssueCommentEvent iceUpdater
	opts                    GithubOpts
	allowedAuthors          []string
	// key is the x509 certificate key as ASCII-armored (PEM) data
	key []byte
}

// GithubOpts provides options for configuring a GitHub hook
type GithubOpts struct {
	AppID               int
	DefaultSharedSecret string
	EmittedEvents       []string
}

type fileGetter func(commit, path string, proj *brigade.Project) ([]byte, error)

type statusCreator func(commit string, proj *brigade.Project, status *github.RepoStatus) error

type iceUpdater func(c *gin.Context, s *githubHook, ice *github.IssueCommentEvent, rev brigade.Revision, proj *brigade.Project, body []byte) (brigade.Revision, []byte)

// NewGithubHookHandler creates a GitHub webhook handler.
func NewGithubHookHandler(s storage.Store, authors []string, x509Key []byte, opts GithubOpts) gin.HandlerFunc {
	gh := &githubHook{
		store:                   s,
		getFile:                 getFileFromGithub,
		createStatus:            setRepoStatus,
		handleIssueCommentEvent: handleIssueCommentEvent,
		allowedAuthors:          authors,
		key:                     x509Key,
		opts:                    opts,
	}

	return gh.Handle
}

// Handle routes a webhook to its appropriate handler.
//
// It does this by sniffing the event from the header, and routing accordingly.
func (s *githubHook) Handle(c *gin.Context) {
	event := c.Request.Header.Get("X-GitHub-Event")
	switch event {
	case "ping":
		log.Print("Received ping from GitHub")
		c.JSON(200, gin.H{"message": "OK"})
		return
	case "issue_comment":
		s.handleIssueComment(c, event)
	default:
		// Issue #127: Don't return an error for unimplemented events.
		log.Printf("Unsupported event %q", event)
		c.JSON(200, gin.H{"message": "Ignored"})
		return
	}
}

// handleIssueComment handles an "issue_comment" event type
func (s *githubHook) handleIssueComment(c *gin.Context, eventType string) {
	body, err := ioutil.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("Failed to read body: %s", err)
		c.JSON(http.StatusBadRequest, gin.H{"status": "Malformed body"})
		return
	}
	defer c.Request.Body.Close()

	e, err := github.ParseWebHook(eventType, body)
	if err != nil {
		log.Printf("Failed to parse body: %s", err)
		c.JSON(http.StatusBadRequest, gin.H{"status": "Malformed body"})
		return
	}

	var action string
	var repo string
	var rev brigade.Revision
	var payload []byte
	var ice *github.IssueCommentEvent

	switch e := e.(type) {
	case *github.IssueCommentEvent:
		ice = e
		action = e.GetAction()
		repo = e.Repo.GetFullName()
	default:
		log.Printf("Failed to parse payload")
		c.JSON(http.StatusBadRequest, gin.H{"status": "Received data is not supported or not valid JSON"})
		return
	}

	proj, err := s.store.GetProject(repo)
	if err != nil {
		log.Printf("Project %q not found. No secret loaded. %s", repo, err)
		c.JSON(http.StatusBadRequest, gin.H{"status": "project not found"})
		return
	}

	var sharedSecret = proj.SharedSecret
	if sharedSecret == "" {
		sharedSecret = s.opts.DefaultSharedSecret
	}
	if sharedSecret == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "No secret is configured for this repo."})
		return
	}

	signature := c.Request.Header.Get(hubSignatureHeader)
	if err := validateSignature(signature, sharedSecret, body); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"status": "malformed signature"})
		return
	}

	if ice != nil && (action == "created" || action == "edited") {
		// If there are Pull Request links, this issue matches a Pull Request,
		// so we should fetch and set corresponding revision values
		prLinks := ice.Issue.GetPullRequestLinks()
		if prLinks != nil {
			// If author association of issue comment is not in allowed list, we return,
			// as we don't wish to populate event with actionable data (for requesting check runs, etc.)
			if assoc := ice.Comment.GetAuthorAssociation(); !s.isAllowedAuthor(assoc) {
				log.Printf("not fetching corresponding pull request as issue comment is from disallowed author %s", assoc)
			} else {
				rev, payload = s.handleIssueCommentEvent(c, s, ice, rev, proj, body)
			}
		}
	}

	// If rev ref still unset, set to master so builds can instantiate
	if rev.Ref == "" {
		rev.Ref = "refs/heads/master"
	}

	// Schedule a build using the raw eventType
	s.build(eventType, rev, payload, proj)
	// For events that have an action, schedule a second build for eventType:action
	if action != "" {
		s.build(fmt.Sprintf("%s:%s", eventType, action), rev, payload, proj)
	}

	c.JSON(http.StatusOK, gin.H{"status": "Complete"})
}

// handleIssueCommentEvent runs further processing with a given github.IssueCommentEvent,
// including extracting data from a corresponding Pull Request and adding GitHub App data
// (App ID, Installation ID, Token, Timeout) to the returned payload body.
//
// This extra context empowers consumers of the resulting Brigade event with the ability
// to (re-)trigger actions on the Pull Request itself, such as (re-)running Check Runs,
// Check Suites or otherwise running jobs that consume/use the PR commit/branch data.
func handleIssueCommentEvent(c *gin.Context, s *githubHook, ice *github.IssueCommentEvent, rev brigade.Revision, proj *brigade.Project, body []byte) (brigade.Revision, []byte) {
	appID := s.opts.AppID
	instID := ice.Installation.GetID()

	if appID == 0 || instID == 0 {
		log.Printf("App ID and Installation ID must both be set. App: %d, Installation: %d", appID, instID)
		c.JSON(http.StatusForbidden, gin.H{"status": ErrAuthFailed})
		return rev, body
	}

	tok, timeout, err := s.installationToken(int(appID), int(instID), proj.Github)
	if err != nil {
		log.Printf("Failed to negotiate a token: %s", err)
		c.JSON(http.StatusForbidden, gin.H{"status": ErrAuthFailed})
		return rev, body
	}

	pullRequest, err := getPRFromIssueComment(c, s, tok, ice, proj)
	if err != nil {
		c.JSON(http.StatusInternalServerError,
			gin.H{"status": "failed to fetch pull request for corresponding issue comment"})
		return rev, body
	}

	// Populate the brigade.Revision, as per usual
	rev.Commit = pullRequest.Head.GetSHA()
	rev.Ref = fmt.Sprintf("refs/pull/%d/head", pullRequest.GetNumber())

	// Here we build/populate Brigade's webhook.Payload object
	//
	// Note we also add commit and branch data here, as neither is
	// included in the github.IssueCommentEvent (here res.Body)
	// The check run utility that requests check runs requires these values
	// and does not have access to he brigade.Revision object above.
	res := &Payload{
		Body:         ice,
		AppID:        appID,
		InstID:       int(instID),
		Type:         "issue_comment",
		Token:        tok,
		TokenExpires: timeout,
		Commit:       rev.Commit,
		Branch:       rev.Ref,
	}

	// Remarshal the body back into JSON
	pl := map[string]interface{}{}
	err = json.Unmarshal(body, &pl)
	res.Body = pl
	if err != nil {
		log.Printf("Failed to re-parse body: %s", err)
		c.JSON(http.StatusBadRequest, gin.H{"status": "Our parser is probably broken"})
		return rev, body
	}

	payload, err := json.Marshal(res)
	if err != nil {
		log.Print(err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "JSON encoding error"})
		return rev, body
	}
	return rev, payload
}

// getPRFromIssueComment fetches a pull request from a corresponding github.IssueCommentEvent
func getPRFromIssueComment(c *gin.Context, s *githubHook, token string, ice *github.IssueCommentEvent, proj *brigade.Project) (*github.PullRequest, error) {
	repo := ice.Repo.GetFullName()

	client, err := InstallationTokenClient(token, proj.Github.BaseURL, proj.Github.UploadURL)
	if err != nil {
		log.Printf("Failed to create a new installation token client: %s", err)
		return nil, ErrAuthFailed
	}

	projectNames := strings.Split(repo, "/")
	if len(projectNames) != 2 {
		log.Printf("Repo %q is invalid. Should be github.com/ORG/NAME.", repo)
		return nil, errors.New("invalid repo name")
	}
	owner, pname := projectNames[0], projectNames[1]

	pullRequest, resp, err := client.PullRequests.Get(c, owner, pname, ice.Issue.GetNumber())
	if err != nil {
		log.Printf("Failed to get pull request: %s", err)
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("Failed to get pull request; http response status code: %d", resp.StatusCode)
		return nil, err
	}

	return pullRequest, nil
}

func (s *githubHook) isAllowedAuthor(author string) bool {
	for _, a := range s.allowedAuthors {
		if a == author {
			return true
		}
	}
	return false
}

func (s *githubHook) shouldEmit(eventType string) bool {
	eventAction := strings.Split(eventType, ":")

	var event, action string
	event = eventAction[0]
	if len(eventAction) > 1 {
		action = eventAction[1]
	}

	for _, e := range s.opts.EmittedEvents {

		if e == "*" || e == event || e == event+":"+action {
			return true
		}
	}
	return false
}

func getFileFromGithub(commit, path string, proj *brigade.Project) ([]byte, error) {
	return GetFileContents(proj, commit, path)
}

func (s *githubHook) build(eventType string, rev brigade.Revision, payload []byte, proj *brigade.Project) error {
	if !s.shouldEmit(eventType) {
		return nil
	}
	b := &brigade.Build{
		ProjectID: proj.ID,
		Type:      eventType,
		Provider:  "github",
		Revision:  &rev,
		Payload:   payload,
	}
	return s.store.CreateBuild(b)
}

// validateSignature compares the salted digest in the header with our own computing of the body.
func validateSignature(signature, secretKey string, payload []byte) error {
	sum := SHA1HMAC([]byte(secretKey), payload)
	if subtle.ConstantTimeCompare([]byte(sum), []byte(signature)) != 1 {
		log.Printf("Expected signature %q (sum), got %q (hub-signature)", sum, signature)
		return errors.New("payload signature check failed")
	}
	return nil
}
