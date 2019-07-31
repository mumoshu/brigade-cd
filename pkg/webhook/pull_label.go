package webhook

import (
	"context"
	"fmt"
	"github.com/brigadecore/brigade/pkg/brigade"
	"gopkg.in/gin-gonic/gin.v1"
	"net/http"
	"os"
	"strconv"
)

type pullLabelHandler struct {
	opts                    GithubOpts
	// key is the x509 certificate key as ASCII-armored (PEM) data
	key   []byte
	token string
}

// NewGithubHookHandler creates a GitHub webhook handler.
func NewPullLabelHandler(x509Key []byte, opts GithubOpts) gin.HandlerFunc {
	h := &pullLabelHandler{
		key:                     x509Key,
		opts:                    opts,
	}

	return h.Handle
}

func (h *pullLabelHandler) Handle(c *gin.Context) {
	client, err := ghClient(brigade.Github{Token: h.token})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init GitHub client: %s", err)
		c.JSON(http.StatusBadRequest, gin.H{"status": "github client error"})
		return
	}
	owner := c.Params.ByName("owner")
	repo := c.Params.ByName("repo")
	pull := c.Params.ByName("pull")
	label := c.Params.ByName("label")
	num, err := strconv.Atoi(pull)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to convert pull request number: %s", err)
		c.JSON(http.StatusBadRequest, gin.H{"status": "conversion error"})
	}
	pr, res, err := client.PullRequests.Get(context.TODO(), owner, repo, num)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resposne: %v", res)
		fmt.Fprintf(os.Stderr, "Failed to get  pull request: %s", err)
		c.JSON(http.StatusBadRequest, gin.H{"status": "pull request fetch error"})
	}
	for _, l := range pr.Labels {
		if *l.Name == label {
			c.JSON(http.StatusOK, gin.H{"status": fmt.Sprintf("pull request has label %q", label)})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"status": fmt.Sprintf("pull request has no label %q", label)})
}
