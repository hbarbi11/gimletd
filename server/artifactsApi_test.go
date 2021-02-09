package server

import (
	"context"
	"encoding/json"
	"github.com/gimlet-io/gimletd/dx"
	"github.com/gimlet-io/gimletd/model"
	"github.com/gimlet-io/gimletd/store"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func Test_saveArtifact(t *testing.T) {
	store := store.NewTest()

	artifactStr := `
{
  "id": "my-app-b2ab0f7a-ca0e-45cf-83a0-cadd94dddeac",
  "version": {
    "repositoryName": "my-app",
    "sha": "ea9ab7cc31b2599bf4afcfd639da516ca27a4780",
    "branch": "master",
    "authorName": "Jane Doe",
    "authorEmail": "jane@doe.org",
    "committerName": "Jane Doe",
    "committerEmail": "jane@doe.org",
    "message": "Bugfix 123",
    "url": "https://github.com/gimlet-io/gimlet-cli/commit/ea9ab7cc31b2599bf4afcfd639da516ca27a4780"
  },
  "items": [
    {
      "name": "CI",
      "url": "https://jenkins.example.com/job/dev/84/display/redirect"
    }
  ]
}
`

	var a dx.Artifact
	json.Unmarshal([]byte(artifactStr), &a)

	_, body, err := testEndpoint(saveArtifact, func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, "store", store)
		return ctx
	}, "/path")
	assert.Nil(t, err)

	var response dx.Artifact
	err = json.Unmarshal([]byte(body), &response)
	assert.Nil(t, err)
	assert.NotEqual(t, response.Created, 0, "should set created time")
}


func Test_getArtifacts(t *testing.T) {
	store := store.NewTest()
	setupArtifacts(store)

	_, body, err := testEndpoint(getArtifacts, func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, "store", store)
		return ctx
	}, "/path")
	assert.Nil(t, err)
	var response []*dx.Artifact
	err = json.Unmarshal([]byte(body), &response)
	assert.Nil(t, err)
	assert.Equal(t, len(response), 2)
}

func Test_getArtifactsLimitOffset(t *testing.T) {
	store := store.NewTest()
	setupArtifacts(store)

	_, body, err := testEndpoint(getArtifacts, func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, "store", store)
		return ctx
	}, "/path?limit=1&offset=1")
	assert.Nil(t, err)
	var response []*dx.Artifact
	err = json.Unmarshal([]byte(body), &response)
	assert.Nil(t, err)
	assert.Equal(t, len(response), 1)
	assert.Equal(t, "2", response[0].Version.SHA)
}

func Test_getArtifactsBranch(t *testing.T) {
	store := store.NewTest()
	setupArtifacts(store)

	_, body, err := testEndpoint(getArtifacts, func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, "store", store)
		return ctx
	}, "/path?branch=bugfix-123")
	assert.Nil(t, err)
	var response []*dx.Artifact
	err = json.Unmarshal([]byte(body), &response)
	assert.Nil(t, err)
	assert.Equal(t, 1, len(response))
	assert.Equal(t, "bugfix-123", response[0].Version.Branch)
}

func Test_getArtifactsApp(t *testing.T) {
	store := store.NewTest()
	setupArtifacts(store)

	_, body, err := testEndpoint(getArtifacts, func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, "store", store)
		return ctx
	}, "/path?app=my-app")
	assert.Nil(t, err)
	var response []*dx.Artifact
	err = json.Unmarshal([]byte(body), &response)
	assert.Nil(t, err)
	assert.Equal(t, 2, len(response))
}

func Test_getArtifactsPR(t *testing.T) {
	store := store.NewTest()
	setupArtifacts(store)

	_, body, err := testEndpoint(getArtifacts, func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, "store", store)
		return ctx
	}, "/path?event=pr")
	assert.Nil(t, err)
	var response []*dx.Artifact
	err = json.Unmarshal([]byte(body), &response)
	assert.Nil(t, err)
	assert.Equal(t, 1, len(response))
	assert.Equal(t, "2", response[0].Version.SHA)
}

func Test_getArtifactsSince(t *testing.T) {
	store := store.NewTest()
	setupArtifacts(store)

	time.Sleep(1 * time.Second)

	since := time.Now().UTC()

	artifactStr := `
{
  "version": {
    "repositoryName": "my-app",
    "sha": "sha-since",
    "branch": "master",
    "authorName": "Jane Doe",
    "authorEmail": "jane@doe.org",
    "committerName": "Jane Doe",
    "committerEmail": "jane@doe.org",
    "message": "Bugfix 123",
    "url": "https://github.com/gimlet-io/gimlet-cli/commit/ea9ab7cc31b2599bf4afcfd639da516ca27a4780"
  },
  "items": [
    {
      "name": "CI",
      "url": "https://jenkins.example.com/job/dev/84/display/redirect"
    }
  ]
}
`

	var a dx.Artifact
	json.Unmarshal([]byte(artifactStr), &a)
	artifactModel, err := model.ToArtifactModel(a)
	if err != nil {
		panic(err)
	}
	_, err = store.CreateArtifact(artifactModel)
	if err != nil {
		panic(err)
	}

	code, body, err := testEndpoint(getArtifacts, func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, "store", store)
		return ctx
	}, "/artifacts?since=" + url.QueryEscape(since.Format(time.RFC3339)))
	assert.Equal(t, http.StatusOK, code)
	var response []*dx.Artifact
	err = json.Unmarshal([]byte(body), &response)
	assert.Nil(t, err)
	assert.Equal(t, 1, len(response))
	assert.Equal(t, "sha-since", response[0].Version.SHA)
}

func setupArtifacts(store *store.Store) {
	artifactStr := `
{
  "id": "my-app-b2ab0f7a-ca0e-45cf-83a0-cadd94dddeac",
  "version": {
    "repositoryName": "my-app",
    "sha": "ea9ab7cc31b2599bf4afcfd639da516ca27a4780",
    "branch": "master",
    "authorName": "Jane Doe",
    "authorEmail": "jane@doe.org",
    "committerName": "Jane Doe",
    "committerEmail": "jane@doe.org",
    "message": "Bugfix 123",
    "url": "https://github.com/gimlet-io/gimlet-cli/commit/ea9ab7cc31b2599bf4afcfd639da516ca27a4780"
  },
  "items": [
    {
      "name": "CI",
      "url": "https://jenkins.example.com/job/dev/84/display/redirect"
    }
  ]
}
`

	var a dx.Artifact
	json.Unmarshal([]byte(artifactStr), &a)
	artifactModel, err := model.ToArtifactModel(a)
	if err != nil {
		panic(err)
	}
	_, err = store.CreateArtifact(artifactModel)
	if err != nil {
		panic(err)
	}

	artifactStr2 := `
{
  "id": "my-app-2",
  "version": {
    "repositoryName": "my-app",
    "sha": "2",
	"event": "pr",
    "branch": "bugfix-123",
    "authorName": "Jane Doe",
    "authorEmail": "jane@doe.org",
    "committerName": "Jane Doe",
    "committerEmail": "jane@doe.org",
    "message": "Bugfix 123",
    "url": "https://github.com/gimlet-io/gimlet-cli/commit/ea9ab7cc31b2599bf4afcfd639da516ca27a4780"
  },
  "items": [
    {
      "name": "CI",
      "url": "https://jenkins.example.com/job/dev/84/display/redirect"
    }
  ]
}
`

	json.Unmarshal([]byte(artifactStr2), &a)
	artifactModel, err = model.ToArtifactModel(a)
	if err != nil {
		panic(err)
	}
	_, err = store.CreateArtifact(artifactModel)
	if err != nil {
		panic(err)
	}
}

type contextFunc func(ctx context.Context) context.Context

func testEndpoint(handlerFunc http.HandlerFunc, cn contextFunc, path string) (int, string, error) {
	// Create a request to pass to our handler. We don't have any query parameters for now, so we'll
	// pass 'nil' as the third parameter.
	req := httptest.NewRequest("GET", path, nil)
	req = req.WithContext(cn(req.Context()))

	// We create a ResponseRecorder (which satisfies http.ResponseWriter) to record the response.
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handlerFunc)
	// Our handlers satisfy http.Handler, so we can call their ServeHTTP method
	// directly and pass in our Request and ResponseRecorder.
	handler.ServeHTTP(rr, req)

	return rr.Code, rr.Body.String(), nil
}
