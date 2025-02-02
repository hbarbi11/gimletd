package main

import (
	"database/sql"
	"encoding/base32"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"strings"

	"github.com/gimlet-io/gimletd/cmd/config"
	"github.com/gimlet-io/gimletd/git/customScm"
	"github.com/gimlet-io/gimletd/git/customScm/customGithub"
	"github.com/gimlet-io/gimletd/git/nativeGit"
	"github.com/gimlet-io/gimletd/model"
	"github.com/gimlet-io/gimletd/notifications"
	"github.com/gimlet-io/gimletd/server"
	"github.com/gimlet-io/gimletd/server/token"
	"github.com/gimlet-io/gimletd/store"
	"github.com/gimlet-io/gimletd/worker"
	"github.com/go-chi/chi"
	"github.com/gorilla/securecookie"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

func main() {
	err := godotenv.Load(".env")
	if err != nil {
		logrus.Warnf("could not load .env file, relying on env vars")
	}

	config, err := config.Environ()
	if err != nil {
		logger := logrus.WithError(err)
		logger.Fatalln("main: invalid configuration")
	}

	initLogging(config)

	if logrus.IsLevelEnabled(logrus.TraceLevel) {
		fmt.Println(config.String())
	}

	store := store.New(config.Database.Driver, config.Database.Config)

	err = setupAdminUser(config, store)
	if err != nil {
		panic(err)
	}

	var tokenManager customScm.NonImpersonatedTokenManager
	if config.Github.AppID != "" {
		tokenManager, err = customGithub.NewGithubOrgTokenManager(config)
		if err != nil {
			panic(err)
		}
	} else {
		logrus.Warnf("Please set Github Application based access for features like deleted branch detection and commit status pushing")
	}

	notificationsManager := notifications.NewManager()
	if config.Notifications.Provider == "slack" {
		notificationsManager.AddProvider(slackNotificationProvider(config))
	}
	if tokenManager != nil {
		notificationsManager.AddProvider(notifications.NewGithubProvider(tokenManager))
	}
	go notificationsManager.Run()

	stopCh := make(chan struct{})
	defer close(stopCh)

	repoCache, err := nativeGit.NewGitopsRepoCache(
		config.RepoCachePath,
		config.GitopsRepo,
		config.GitopsRepoDeployKeyPath,
		stopCh,
	)
	if err != nil {
		panic(err)
	}
	go repoCache.Run()
	logrus.Info("repo cache initialized")

	if config.GitopsRepo != "" &&
		config.GitopsRepoDeployKeyPath != "" {
		gitopsWorker := worker.NewGitopsWorker(
			store,
			config.GitopsRepo,
			config.GitopsRepoDeployKeyPath,
			tokenManager,
			notificationsManager,
			eventsProcessed,
			repoCache,
		)
		go gitopsWorker.Run()
		logrus.Info("Gitops worker started")
	} else {
		logrus.Warn("Not starting GitOps worker. GITOPS_REPO and GITOPS_REPO_DEPLOY_KEY_PATH must be set to start GitOps worker")
	}

	if config.ReleaseStats == "enabled" {
		releaseStateWorker := &worker.ReleaseStateWorker{
			GitopsRepo: config.GitopsRepo,
			RepoCache:  repoCache,
			Releases:   releases,
			Perf:       perf,
		}
		go releaseStateWorker.Run()
	}

	if tokenManager != nil {
		branchDeleteEventWorker := worker.NewBranchDeleteEventWorker(
			tokenManager,
			config.RepoCachePath,
			store,
		)
		go branchDeleteEventWorker.Run()
	}

	metricsRouter := chi.NewRouter()
	metricsRouter.Get("/metrics", promhttp.Handler().ServeHTTP)
	go http.ListenAndServe(":8889", metricsRouter)

	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	r := server.SetupRouter(config, store, notificationsManager, repoCache, perf)
	err = http.ListenAndServe(":8888", r)
	if err != nil {
		panic(err)
	}
}

func slackNotificationProvider(config *config.Config) *notifications.SlackProvider {
	channelMap := map[string]string{}
	if config.Notifications.ChannelMapping != "" {
		pairs := strings.Split(config.Notifications.ChannelMapping, ",")
		for _, p := range pairs {
			keyValue := strings.Split(p, "=")
			channelMap[keyValue[0]] = keyValue[1]
		}
	}
	return &notifications.SlackProvider{
		Token:          config.Notifications.Token,
		ChannelMapping: channelMap,
		DefaultChannel: config.Notifications.DefaultChannel,
	}
}

// helper function configures the logging.
func initLogging(c *config.Config) {
	if c.Logging.Debug {
		logrus.SetLevel(logrus.DebugLevel)
	}
	if c.Logging.Trace {
		logrus.SetLevel(logrus.TraceLevel)
	}
	if c.Logging.Text {
		logrus.SetFormatter(&logrus.TextFormatter{
			ForceColors:   c.Logging.Color,
			DisableColors: !c.Logging.Color,
		})
	} else {
		logrus.SetFormatter(&logrus.JSONFormatter{
			PrettyPrint: c.Logging.Pretty,
		})
	}
}

// Creates an admin user and prints her access token, in case there are no users in the database
func setupAdminUser(config *config.Config, store *store.Store) error {
	admin, err := store.User("admin")

	if err == sql.ErrNoRows {
		admin := &model.User{
			Login: "admin",
			Secret: base32.StdEncoding.EncodeToString(
				securecookie.GenerateRandomKey(32),
			),
			Admin: true,
		}
		err = store.CreateUser(admin)
		if err != nil {
			return fmt.Errorf("couldn't create user admin user %s", err)
		}
		err = printAdminToken(admin)
		if err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("couldn't list users to create admin user %s", err)
	}

	if config.PrintAdminToken {
		err = printAdminToken(admin)
		if err != nil {
			return err
		}
	} else {
		logrus.Infof("Admin token was already printed, use the PRINT_ADMIN_TOKEN=true env var to print it again")
	}

	return nil
}

func printAdminToken(admin *model.User) error {
	token := token.New(token.UserToken, admin.Login)
	tokenStr, err := token.Sign(admin.Secret)
	if err != nil {
		return fmt.Errorf("couldn't create admin token %s", err)
	}
	logrus.Infof("Admin token: %s", tokenStr)

	return nil
}
