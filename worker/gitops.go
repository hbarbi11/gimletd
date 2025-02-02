package worker

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/gimlet-io/gimletd/dx/kustomize"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gimlet-io/gimletd/dx"
	"github.com/gimlet-io/gimletd/dx/helm"
	"github.com/gimlet-io/gimletd/git/customScm"
	"github.com/gimlet-io/gimletd/git/nativeGit"
	"github.com/gimlet-io/gimletd/model"
	"github.com/gimlet-io/gimletd/notifications"
	"github.com/gimlet-io/gimletd/store"
	"github.com/gimlet-io/gimletd/worker/events"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/gobwas/glob"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

type GitopsWorker struct {
	store                   *store.Store
	gitopsRepo              string
	gitopsRepoDeployKeyPath string
	tokenManager            customScm.NonImpersonatedTokenManager
	notificationsManager    notifications.Manager
	eventsProcessed         prometheus.Counter
	repoCache               *nativeGit.GitopsRepoCache
}

func NewGitopsWorker(
	store *store.Store,
	gitopsRepo string,
	gitopsRepoDeployKeyPath string,
	tokenManager customScm.NonImpersonatedTokenManager,
	notificationsManager notifications.Manager,
	eventsProcessed prometheus.Counter,
	repoCache *nativeGit.GitopsRepoCache,
) *GitopsWorker {
	return &GitopsWorker{
		store:                   store,
		gitopsRepo:              gitopsRepo,
		gitopsRepoDeployKeyPath: gitopsRepoDeployKeyPath,
		notificationsManager:    notificationsManager,
		tokenManager:            tokenManager,
		eventsProcessed:         eventsProcessed,
		repoCache:               repoCache,
	}
}

func (w *GitopsWorker) Run() {
	for {
		events, err := w.store.UnprocessedEvents()
		if err != nil {
			logrus.Errorf("Could not fetch unprocessed events %s", err.Error())
			time.Sleep(1 * time.Second)
			continue
		}

		for _, event := range events {
			w.eventsProcessed.Inc()
			processEvent(w.store,
				w.gitopsRepo,
				w.gitopsRepoDeployKeyPath,
				w.tokenManager,
				event,
				w.notificationsManager,
				w.repoCache,
			)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func processEvent(
	store *store.Store,
	gitopsRepo string,
	gitopsRepoDeployKeyPath string,
	tokenManager customScm.NonImpersonatedTokenManager,
	event *model.Event,
	notificationsManager notifications.Manager,
	repoCache *nativeGit.GitopsRepoCache,
) {
	var token string
	if tokenManager != nil { // only needed for private helm charts
		token, _, _ = tokenManager.Token()
	}

	// process event based on type
	var err error
	var gitopsEvents []*events.DeployEvent
	var rollbackEvent *events.RollbackEvent
	var deleteEvents []*events.DeleteEvent
	switch event.Type {
	case model.TypeArtifact:
		gitopsEvents, err = processArtifactEvent(
			gitopsRepo,
			repoCache,
			gitopsRepoDeployKeyPath,
			token,
			event,
			store,
		)
	case model.TypeRelease:
		gitopsEvents, err = processReleaseEvent(
			store,
			gitopsRepo,
			repoCache,
			gitopsRepoDeployKeyPath,
			token,
			event,
		)
	case model.TypeRollback:
		rollbackEvent, err = processRollbackEvent(
			gitopsRepo,
			gitopsRepoDeployKeyPath,
			repoCache,
			event,
		)
		notificationsManager.Broadcast(notifications.MessageFromRollbackEvent(rollbackEvent))
		for _, sha := range rollbackEvent.GitopsRefs {
			setGitopsHashOnEvent(event, sha)
		}
	case model.TypeBranchDeleted:
		deleteEvents, err = processBranchDeletedEvent(
			gitopsRepo,
			gitopsRepoDeployKeyPath,
			repoCache,
			event,
		)
		for _, deleteEvent := range deleteEvents {
			notificationsManager.Broadcast(notifications.MessageFromDeleteEvent(deleteEvent))
			setGitopsHashOnEvent(event, deleteEvent.GitopsRef)
		}
	}

	// send out notifications based on gitops events
	for _, gitopsEvent := range gitopsEvents {
		notificationsManager.Broadcast(notifications.MessageFromGitOpsEvent(gitopsEvent))
	}

	// record gitops hashes on events
	for _, gitopsEvent := range gitopsEvents {
		setGitopsHashOnEvent(event, gitopsEvent.GitopsRef)
	}

	// store event state
	if err != nil {
		logrus.Errorf("error in processing event: %s", err.Error())
		event.Status = model.StatusError
		event.StatusDesc = err.Error()
		err := updateEvent(store, event)
		if err != nil {
			logrus.Warnf("could not update event status %v", err)
		}
	} else {
		event.Status = model.StatusProcessed
		err := updateEvent(store, event)
		if err != nil {
			logrus.Warnf("could not update event status %v", err)
		}
	}
}

func processBranchDeletedEvent(
	gitopsRepo string,
	gitopsRepoDeployKeyPath string,
	gitopsRepoCache *nativeGit.GitopsRepoCache,
	event *model.Event,
) ([]*events.DeleteEvent, error) {
	var deletedEvents []*events.DeleteEvent
	var branchDeletedEvent events.BranchDeletedEvent
	err := json.Unmarshal([]byte(event.Blob), &branchDeletedEvent)
	if err != nil {
		return nil, fmt.Errorf("cannot parse delete request with id: %s", event.ID)
	}

	for _, env := range branchDeletedEvent.Manifests {
		if env.Cleanup == nil {
			continue
		}

		gitopsEvent := &events.DeleteEvent{
			Env:         env.Env,
			App:         env.Cleanup.AppToCleanup,
			TriggeredBy: "policy",
			Status:      events.Success,
			GitopsRepo:  gitopsRepo,

			BranchDeletedEvent: branchDeletedEvent,
		}

		err := env.Cleanup.ResolveVars(map[string]string{
			"BRANCH": branchDeletedEvent.Branch,
		})
		if err != nil {
			gitopsEvent.Status = events.Failure
			gitopsEvent.StatusDesc = err.Error()
			return []*events.DeleteEvent{gitopsEvent}, err
		}
		gitopsEvent.App = env.Cleanup.AppToCleanup // vars are resolved now

		if !cleanupTrigger(branchDeletedEvent.Branch, env.Cleanup) {
			continue
		}

		gitopsEvent, err = cloneTemplateDeleteAndPush(
			gitopsRepoCache,
			gitopsRepoDeployKeyPath,
			env.Cleanup,
			env.Env,
			"policy",
			gitopsEvent,
		)
		if gitopsEvent != nil {
			deletedEvents = append(deletedEvents, gitopsEvent)
		}
		if err != nil {
			return deletedEvents, err
		}
	}

	return deletedEvents, err
}

func setGitopsHashOnEvent(event *model.Event, gitopsSha string) {
	if gitopsSha == "" {
		return
	}

	if event.GitopsHashes == nil {
		event.GitopsHashes = []string{}
	}

	event.GitopsHashes = append(event.GitopsHashes, gitopsSha)
}

func processReleaseEvent(
	store *store.Store,
	gitopsRepo string,
	gitopsRepoCache *nativeGit.GitopsRepoCache,
	gitopsRepoDeployKeyPath string,
	githubChartAccessToken string,
	event *model.Event,
) ([]*events.DeployEvent, error) {
	var gitopsEvents []*events.DeployEvent
	var releaseRequest dx.ReleaseRequest
	err := json.Unmarshal([]byte(event.Blob), &releaseRequest)
	if err != nil {
		return gitopsEvents, fmt.Errorf("cannot parse release request with id: %s", event.ID)
	}

	artifactEvent, err := store.Artifact(releaseRequest.ArtifactID)
	if err != nil {
		return gitopsEvents, fmt.Errorf("cannot find artifact with id: %s", event.ArtifactID)
	}
	artifact, err := model.ToArtifact(artifactEvent)
	if err != nil {
		return gitopsEvents, fmt.Errorf("cannot parse artifact %s", err.Error())
	}

	for _, env := range artifact.Environments {
		if env.Env != releaseRequest.Env {
			continue
		}
		env.ResolveVars(artifact.Vars())
		if releaseRequest.App != "" &&
			env.App != releaseRequest.App {
			continue
		}

		gitopsEvent, err := cloneTemplateWriteAndPush(
			gitopsRepo,
			gitopsRepoCache,
			gitopsRepoDeployKeyPath,
			githubChartAccessToken,
			artifact,
			env,
			releaseRequest.TriggeredBy,
		)
		gitopsEvents = append(gitopsEvents, gitopsEvent)
		if err != nil {
			return gitopsEvents, err
		}
	}

	return gitopsEvents, nil
}

func processRollbackEvent(
	gitopsRepo string,
	gitopsRepoDeployKeyPath string,
	gitopsRepoCache *nativeGit.GitopsRepoCache,
	event *model.Event,
) (*events.RollbackEvent, error) {
	var rollbackRequest dx.RollbackRequest
	err := json.Unmarshal([]byte(event.Blob), &rollbackRequest)
	if err != nil {
		return nil, fmt.Errorf("cannot parse release request with id: %s", event.ID)
	}

	rollbackEvent := &events.RollbackEvent{
		RollbackRequest: &rollbackRequest,
		GitopsRepo:      gitopsRepo,
	}

	t0 := time.Now().UnixNano()
	repo, repoTmpPath, err := gitopsRepoCache.InstanceForWrite()
	logrus.Infof("Obtaining instance for write took %d", (time.Now().UnixNano()-t0)/1000/1000)
	defer nativeGit.TmpFsCleanup(repoTmpPath)
	if err != nil {
		rollbackEvent.Status = events.Failure
		rollbackEvent.StatusDesc = err.Error()
		return rollbackEvent, err
	}

	headSha, _ := repo.Head()

	err = revertTo(
		rollbackRequest.Env,
		rollbackRequest.App,
		repo,
		repoTmpPath,
		rollbackRequest.TargetSHA,
	)
	if err != nil {
		rollbackEvent.Status = events.Failure
		rollbackEvent.StatusDesc = err.Error()
		return rollbackEvent, err
	}

	hashes, err := shasSince(repo, headSha.Hash().String())
	if err != nil {
		rollbackEvent.Status = events.Failure
		rollbackEvent.StatusDesc = err.Error()
		return rollbackEvent, err
	}

	head, _ := repo.Head()
	err = nativeGit.NativePush(repoTmpPath, gitopsRepoDeployKeyPath, head.Name().Short())
	if err != nil {
		rollbackEvent.Status = events.Failure
		rollbackEvent.StatusDesc = err.Error()
		return rollbackEvent, err
	}
	gitopsRepoCache.Invalidate()

	rollbackEvent.GitopsRefs = hashes
	rollbackEvent.Status = events.Success
	return rollbackEvent, nil
}

func shasSince(repo *git.Repository, since string) ([]string, error) {
	var hashes []string
	commitWalker, err := repo.Log(&git.LogOptions{})
	if err != nil {
		return hashes, fmt.Errorf("cannot walk commits: %s", err)
	}

	err = commitWalker.ForEach(func(c *object.Commit) error {
		if c.Hash.String() == since {
			return fmt.Errorf("%s", "FOUND")
		}
		hashes = append(hashes, c.Hash.String())
		return nil
	})
	if err != nil &&
		err.Error() != "EOF" &&
		err.Error() != "FOUND" {
		return hashes, fmt.Errorf("cannot walk commits: %s", err)
	}

	return hashes, nil
}

func processArtifactEvent(
	gitopsRepo string,
	gitopsRepoCache *nativeGit.GitopsRepoCache,
	gitopsRepoDeployKeyPath string,
	githubChartAccessToken string,
	event *model.Event,
	dao *store.Store,
) ([]*events.DeployEvent, error) {
	var gitopsEvents []*events.DeployEvent
	artifact, err := model.ToArtifact(event)
	if err != nil {
		return gitopsEvents, fmt.Errorf("cannot parse artifact %s", err.Error())
	}

	if artifact.HasCleanupPolicy() {
		keepReposWithCleanupPolicyUpToDate(dao, artifact)
	}

	for _, env := range artifact.Environments {
		if !deployTrigger(artifact, env.Deploy) {
			continue
		}

		gitopsEvent, err := cloneTemplateWriteAndPush(
			gitopsRepo,
			gitopsRepoCache,
			gitopsRepoDeployKeyPath,
			githubChartAccessToken,
			artifact,
			env,
			"policy",
		)
		gitopsEvents = append(gitopsEvents, gitopsEvent)
		if err != nil {
			return gitopsEvents, err
		}
	}

	return gitopsEvents, nil
}

func keepReposWithCleanupPolicyUpToDate(dao *store.Store, artifact *dx.Artifact) {
	reposWithCleanupPolicy, err := dao.ReposWithCleanupPolicy()
	if err != nil && err != sql.ErrNoRows {
		logrus.Warnf("could not load repos with cleanup policy: %s", err)
	}

	repoIsNew := true
	for _, r := range reposWithCleanupPolicy {
		if r == artifact.Version.RepositoryName {
			repoIsNew = false
			break
		}
	}
	if repoIsNew {
		reposWithCleanupPolicy = append(reposWithCleanupPolicy, artifact.Version.RepositoryName)
		err = dao.SaveReposWithCleanupPolicy(reposWithCleanupPolicy)
		if err != nil {
			logrus.Warnf("could not update repos with cleanup policy: %s", err)
		}
	}
}

func cloneTemplateWriteAndPush(
	gitopsRepo string,
	gitopsRepoCache *nativeGit.GitopsRepoCache,
	gitopsRepoDeployKeyPath string,
	githubChartAccessToken string,
	artifact *dx.Artifact,
	env *dx.Manifest,
	triggeredBy string,
) (*events.DeployEvent, error) {
	gitopsEvent := &events.DeployEvent{
		Manifest:    env,
		Artifact:    artifact,
		TriggeredBy: triggeredBy,
		Status:      events.Success,
		GitopsRepo:  gitopsRepo,
	}

	repo, repoTmpPath, err := gitopsRepoCache.InstanceForWrite()
	defer nativeGit.TmpFsCleanup(repoTmpPath)
	if err != nil {
		gitopsEvent.Status = events.Failure
		gitopsEvent.StatusDesc = err.Error()
		return gitopsEvent, err
	}

	err = env.ResolveVars(artifact.Vars())
	if err != nil {
		err = fmt.Errorf("cannot resolve manifest vars %s", err.Error())
		gitopsEvent.Status = events.Failure
		gitopsEvent.StatusDesc = err.Error()
		return gitopsEvent, err
	}

	releaseMeta := &dx.Release{
		App:         env.App,
		Env:         env.Env,
		ArtifactID:  artifact.ID,
		Version:     &artifact.Version,
		TriggeredBy: triggeredBy,
	}

	sha, err := gitopsTemplateAndWrite(
		repo,
		env,
		releaseMeta,
		githubChartAccessToken,
	)
	if err != nil {
		gitopsEvent.Status = events.Failure
		gitopsEvent.StatusDesc = err.Error()
		return gitopsEvent, err
	}

	if sha != "" { // if there is a change to push
		head, _ := repo.Head()

		operation := func() error {
			return nativeGit.NativePush(repoTmpPath, gitopsRepoDeployKeyPath, head.Name().Short())
		}
		backoffStrategy := backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 5)
		err := backoff.Retry(operation, backoffStrategy)
		if err != nil {
			gitopsEvent.Status = events.Failure
			gitopsEvent.StatusDesc = err.Error()
			return gitopsEvent, err
		}
		gitopsRepoCache.Invalidate()

		gitopsEvent.GitopsRef = sha
	}

	return gitopsEvent, nil
}

func cloneTemplateDeleteAndPush(
	gitopsRepoCache *nativeGit.GitopsRepoCache,
	gitopsRepoDeployKeyPath string,
	cleanupPolicy *dx.Cleanup,
	env string,
	triggeredBy string,
	gitopsEvent *events.DeleteEvent,
) (*events.DeleteEvent, error) {
	repo, repoTmpPath, err := gitopsRepoCache.InstanceForWrite()
	defer nativeGit.TmpFsCleanup(repoTmpPath)
	if err != nil {
		gitopsEvent.Status = events.Failure
		gitopsEvent.StatusDesc = err.Error()
		return gitopsEvent, err
	}

	err = nativeGit.DelDir(repo, filepath.Join(env, cleanupPolicy.AppToCleanup))
	if err != nil {
		gitopsEvent.Status = events.Failure
		gitopsEvent.StatusDesc = err.Error()
		return gitopsEvent, err
	}

	empty, err := nativeGit.NothingToCommit(repo)
	if err != nil {
		gitopsEvent.Status = events.Failure
		gitopsEvent.StatusDesc = err.Error()
		return gitopsEvent, err
	}
	if empty {
		return nil, nil
	}

	gitMessage := fmt.Sprintf("[GimletD delete] %s/%s deleted by %s", env, cleanupPolicy.AppToCleanup, triggeredBy)
	sha, err := nativeGit.Commit(repo, gitMessage)

	if sha != "" { // if there is a change to push
		err = nativeGit.Push(repo, gitopsRepoDeployKeyPath)
		if err != nil {
			gitopsEvent.Status = events.Failure
			gitopsEvent.StatusDesc = err.Error()
			return gitopsEvent, err
		}
		gitopsRepoCache.Invalidate()

		gitopsEvent.GitopsRef = sha
	}

	return gitopsEvent, nil
}

func revertTo(env string, app string, repo *git.Repository, repoTmpPath string, sha string) error {
	path := fmt.Sprintf("%s/%s", env, app)
	commits, err := repo.Log(&git.LogOptions{})
	if err != nil {
		return errors.WithMessage(err, "could not walk commits")
	}
	commits = nativeGit.NewCommitDirIterFromIter(path, commits, repo)

	commitsToRevert := []*object.Commit{}
	err = commits.ForEach(func(c *object.Commit) error {
		if c.Hash.String() == sha {
			return fmt.Errorf("EOF")
		}

		if !nativeGit.RollbackCommit(c) {
			commitsToRevert = append(commitsToRevert, c)
		}
		return nil
	})
	if err != nil && err.Error() != "EOF" {
		return err
	}

	for _, commit := range commitsToRevert {
		hasBeenReverted, err := nativeGit.HasBeenReverted(repo, commit, env, app)
		if !hasBeenReverted {
			logrus.Infof("reverting %s", commit.Hash.String())
			err = nativeGit.NativeRevert(repoTmpPath, commit.Hash.String())
			if err != nil {
				return errors.WithMessage(err, "could not revert")
			}
		}
	}
	return nil
}

func updateEvent(store *store.Store, event *model.Event) error {
	gitopsHashesString, err := json.Marshal(event.GitopsHashes)
	if err != nil {
		return err
	}
	return store.UpdateEventStatus(event.ID, event.Status, event.StatusDesc, string(gitopsHashesString))
}

func gitopsTemplateAndWrite(
	repo *git.Repository,
	env *dx.Manifest,
	release *dx.Release,
	tokenForChartClone string,
) (string, error) {
	if strings.HasPrefix(env.Chart.Name, "git@") {
		return "", fmt.Errorf("only HTTPS git repo urls supported in GimletD for git based charts")
	}
	if strings.Contains(env.Chart.Name, ".git") {
		t0 := time.Now().UnixNano()
		tmpChartDir, err := helm.CloneChartFromRepo(*env, tokenForChartClone)
		if err != nil {
			return "", fmt.Errorf("cannot fetch chart from git %s", err.Error())
		}
		logrus.Infof("Cloning chart took %d", (time.Now().UnixNano()-t0)/1000/1000)
		env.Chart.Name = tmpChartDir
		defer os.RemoveAll(tmpChartDir)
	}

	t0 := time.Now().UnixNano()
	templatedManifests, err := helm.HelmTemplate(*env)
	if err != nil {
		return "", fmt.Errorf("cannot run helm template %s", err.Error())
	}
	logrus.Infof("Helm template took %d", (time.Now().UnixNano()-t0)/1000/1000)

	if env.StrategicMergePatches != "" {
		templatedManifests, err = kustomize.ApplyPatches(env.StrategicMergePatches, templatedManifests)
		if err != nil {
			return "", fmt.Errorf("cannot apply Kustomize patches to chart %s", err.Error())
		}
	}

	files := helm.SplitHelmOutput(map[string]string{"manifest.yaml": templatedManifests})

	releaseString, err := json.Marshal(release)
	if err != nil {
		return "", fmt.Errorf("cannot marshal release meta data %s", err.Error())
	}

	sha, err := nativeGit.CommitFilesToGit(repo, files, env.Env, env.App, "automated deploy", string(releaseString))
	if err != nil {
		return "", fmt.Errorf("cannot write to git: %s", err.Error())
	}

	return sha, nil
}

func deployTrigger(artifactToCheck *dx.Artifact, deployPolicy *dx.Deploy) bool {
	if deployPolicy == nil {
		return false
	}

	if deployPolicy.Branch == "" &&
		deployPolicy.Event == nil &&
		deployPolicy.Tag == "" {
		return false
	}

	if deployPolicy.Branch != "" &&
		(deployPolicy.Event == nil || *deployPolicy.Event != *dx.PushPtr() && *deployPolicy.Event != *dx.PRPtr()) {
		return false
	}

	if deployPolicy.Tag != "" &&
		(deployPolicy.Event == nil || *deployPolicy.Event != *dx.TagPtr()) {
		return false
	}

	if deployPolicy.Tag != "" {
		negate := false
		tag := deployPolicy.Branch
		if strings.HasPrefix(deployPolicy.Tag, "!") {
			negate = true
			tag = deployPolicy.Tag[1:]
		}
		g := glob.MustCompile(deployPolicy.Tag)

		exactMatch := tag == artifactToCheck.Version.Tag
		patternMatch := g.Match(artifactToCheck.Version.Tag)

		match := exactMatch || patternMatch

		if negate && match {
			return false
		}
		if !negate && !match {
			return false
		}
	}

	if deployPolicy.Branch != "" {
		negate := false
		branch := deployPolicy.Branch
		if strings.HasPrefix(deployPolicy.Branch, "!") {
			negate = true
			branch = deployPolicy.Branch[1:]
		}
		g := glob.MustCompile(branch)

		exactMatch := branch == artifactToCheck.Version.Branch
		patternMatch := g.Match(artifactToCheck.Version.Branch)

		match := exactMatch || patternMatch

		if negate && match {
			return false
		}
		if !negate && !match {
			return false
		}
	}

	if deployPolicy.Event != nil {
		if *deployPolicy.Event != artifactToCheck.Version.Event {
			return false
		}
	}

	return true
}

func cleanupTrigger(branch string, cleanupPolicy *dx.Cleanup) bool {
	if cleanupPolicy == nil {
		return false
	}

	if cleanupPolicy.Branch == "" {
		return false
	}

	if cleanupPolicy.AppToCleanup == "" {
		return false
	}

	negate := false
	policyBranch := cleanupPolicy.Branch
	if strings.HasPrefix(cleanupPolicy.Branch, "!") {
		negate = true
		branch = cleanupPolicy.Branch[1:]
	}

	g := glob.MustCompile(policyBranch)

	exactMatch := branch == policyBranch
	patternMatch := g.Match(branch)

	match := exactMatch || patternMatch

	if negate && !match {
		return true
	}
	if !negate && match {
		return true
	}

	return false
}
