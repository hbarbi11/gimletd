package notifications

import (
	"context"
	"fmt"
	githubLib "github.com/google/go-github/v33/github"
	"golang.org/x/oauth2"
	"strings"
	"time"
)

type github struct {
	client *githubLib.Client
}

func newGithubProvider(token string) *github {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	client := githubLib.NewClient(tc)
	return &github{
		client: client,
	}
}

func (g *github) send(msg Message) error {
	status, err := msg.AsGithubStatus()
	if err != nil {
		return fmt.Errorf("cannot create github status message: %s", err)
	}

	if status == nil {
		return nil
	}

	repositoryName := msg.RepositoryName()
	parts := strings.Split(repositoryName, "/")
	if len(parts) != 2 {
		return fmt.Errorf("cannot determine repo owner and name")
	}
	owner := parts[0]
	repo := parts[1]

	sha := msg.SHA()

	return g.post(owner, repo, sha, status)
}

func (g *github) post(owner string, repo string, sha string, status *githubLib.RepoStatus) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	opts := &githubLib.ListOptions{PerPage: 50}
	statuses, _, err := g.client.Repositories.ListStatuses(ctx, owner, repo, sha, opts)
	if err != nil {
		return fmt.Errorf("could not list commit statuses: %v", err)
	}
	if statusExists(statuses, status) {
		return nil
	}

	_, _, err = g.client.Repositories.CreateStatus(ctx, owner, repo, sha, status)
	if err != nil {
		return fmt.Errorf("could not create commit status: %v", err)
	}

	return nil
}

func statusExists(statuses []*githubLib.RepoStatus, status *githubLib.RepoStatus) bool {
	for _, s := range statuses {
		if *s.Context == *status.Context {
			if *s.State == *status.State && *s.Description == *status.Description {
				return true
			}

			return false
		}
	}

	return false
}
