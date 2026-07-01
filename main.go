// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	lib "github.com/bborbe/agent"
	boltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	libkafka "github.com/bborbe/kafka"
	"github.com/bborbe/log"
	libmetrics "github.com/bborbe/metrics"
	"github.com/bborbe/run"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	bolt "go.etcd.io/bbolt"

	"github.com/bborbe/agent-task-controller/pkg/factory"
	"github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/prcomment"
	"github.com/bborbe/agent-task-controller/pkg/publisher"
	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/routing"
	"github.com/bborbe/agent-task-controller/pkg/scanner"
	pkgsync "github.com/bborbe/agent-task-controller/pkg/sync"
)

const vaultLocalPath = "/data/vault"

const githubAPIBaseURL = "https://api.github.com"

func main() {
	app := &application{}
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

type application struct {
	SentryDSN                string            `required:"true"  arg:"sentry-dsn"                  env:"SENTRY_DSN"                  usage:"SentryDSN"                                                                                                                                               display:"length"`
	SentryProxy              string            `required:"false" arg:"sentry-proxy"                env:"SENTRY_PROXY"                usage:"Sentry Proxy"`
	Listen                   string            `required:"true"  arg:"listen"                      env:"LISTEN"                      usage:"address to listen to"`
	KafkaBrokers             string            `required:"true"  arg:"kafka-brokers"               env:"KAFKA_BROKERS"               usage:"comma-separated Kafka broker addresses"`
	Branch                   base.Branch       `required:"true"  arg:"branch"                      env:"BRANCH"                      usage:"Kafka topic prefix branch (develop/live)"`
	PollInterval             time.Duration     `required:"false" arg:"poll-interval"               env:"POLL_INTERVAL"               usage:"vault polling interval"                                                                                                                                                   default:"60s"`
	TaskDir                  string            `required:"true"  arg:"task-dir"                    env:"TASK_DIR"                    usage:"task directory within vault (per-vault convention: openclaw=tasks, personal=24 Tasks)"`
	DataDir                  string            `required:"true"  arg:"data-dir"                    env:"DATA_DIR"                    usage:"directory for BoltDB offset storage"`
	NoSync                   bool              `required:"false" arg:"no-sync"                     env:"NO_SYNC"                     usage:"disable BoltDB fsync (for testing only)"`
	GitRestURL               string            `required:"false" arg:"git-rest-url"                env:"GIT_REST_URL"                usage:"git-rest HTTP API base URL"                                                                                                                                               default:"http://vault-obsidian-openclaw:9090"`
	GatewaySecret            string            `required:"false" arg:"gateway-secret"              env:"GATEWAY_SECRET"              usage:"shared secret for git-rest gateway auth (sent as X-Gateway-Secret header)"                                                                               display:"length" default:""`
	BuildGitVersion          string            `required:"false" arg:"build-git-version"           env:"BUILD_GIT_VERSION"           usage:"Build Git version (git describe --tags --always --dirty)"                                                                                                                 default:"dev"`
	BuildGitCommit           string            `required:"false" arg:"build-git-commit"            env:"BUILD_GIT_COMMIT"            usage:"Build Git commit hash"                                                                                                                                                    default:"none"`
	BuildDate                *libtime.DateTime `required:"false" arg:"build-date"                  env:"BUILD_DATE"                  usage:"Build timestamp (RFC3339)"`
	VaultName                string            `required:"true"  arg:"vault-name"                  env:"VAULT_NAME"                  usage:"vault slug this controller serves (e.g. openclaw, personal); legacy empty targetVault defaults to openclaw"`
	AutoInjectTaskIdentifier string            `required:"true"  arg:"auto-inject-task-identifier" env:"AUTO_INJECT_TASK_IDENTIFIER" usage:"allow this replica to backfill missing/invalid task_identifier fields (set true on exactly one replica per shared vault; false on all others); required"`
	GitHubToken             string            `required:"false" arg:"github-token"                env:"GITHUB_TOKEN"                usage:"GitHub token with pull-requests:write scope for posting planning-retry COMMENT reviews; empty disables the comment (frontmatter escalation still fires)" display:"length" default:""`
}

//nolint:funlen // +6 lines from spec-043 metrics.New() passed to scanner + sync loop; extraction would split tightly-coupled wiring.
func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
	if err := routing.ValidateVaultName(ctx, a.VaultName); err != nil {
		return err
	}
	autoInject, err := strconv.ParseBool(a.AutoInjectTaskIdentifier)
	if err != nil {
		return errors.Errorf(
			ctx,
			"AUTO_INJECT_TASK_IDENTIFIER must be parseable as bool (true/false), got %q",
			a.AutoInjectTaskIdentifier,
		)
	}
	libmetrics.NewBuildInfoMetrics().SetBuildInfo(a.BuildGitVersion, a.BuildGitCommit, a.BuildDate)
	glog.V(1).
		Infof("agent-task-controller started version=%s commit=%s", a.BuildGitVersion, a.BuildGitCommit)

	if a.GitRestURL == "" {
		return errors.Errorf(ctx, "GIT_REST_URL is required")
	}
	restClient := gitrestclient.NewGitRestClient(
		a.GitRestURL, a.GatewaySecret, "agent-task-controller", metrics.New(),
	)
	gitClient := gitrestclient.NewGitClient(restClient, vaultLocalPath)
	if err := gitClient.EnsureCloned(ctx); err != nil {
		return errors.Wrapf(ctx, err, "probe git-rest readiness")
	}
	glog.V(1).Infof("using git-rest HTTP API at %s", a.GitRestURL)

	syncProducer, err := libkafka.NewSyncProducer(
		ctx,
		libkafka.ParseBrokersFromString(a.KafkaBrokers),
	)
	if err != nil {
		return errors.Wrapf(ctx, err, "create kafka sync producer")
	}
	defer syncProducer.Close()

	eventObjectSender := cdb.NewEventObjectSender(
		libkafka.NewJSONSender(syncProducer, log.DefaultSamplerFactory),
		a.Branch,
		log.DefaultSamplerFactory,
	)

	currentDateTime := libtime.NewCurrentDateTime()

	prCommenter := prcomment.NewPRCommenter(&http.Client{Timeout: 30 * time.Second}, githubAPIBaseURL, a.GitHubToken)

	trigger := make(chan struct{}, 1)
	syncLoop := pkgsync.NewSyncLoop(
		scanner.NewGitRestVaultScanner(
			gitClient,
			a.TaskDir,
			a.PollInterval,
			trigger,
			metrics.New(),
			autoInject,
		),
		publisher.NewTaskPublisher(eventObjectSender, lib.TaskV1SchemaID, currentDateTime),
		trigger,
		metrics.New(),
	)

	var boltOptions []boltkv.ChangeOptions
	if a.NoSync {
		boltOptions = append(boltOptions, func(opts *bolt.Options) {
			opts.NoSync = true
		})
	}
	db, err := boltkv.OpenDir(ctx, a.DataDir, boltOptions...)
	if err != nil {
		return errors.Wrapf(ctx, err, "open boltkv dir %s", a.DataDir)
	}
	defer db.Close()

	saramaClientProvider, err := libkafka.NewSaramaClientProviderByType(
		ctx,
		libkafka.SaramaClientProviderTypeReused,
		libkafka.ParseBrokersFromString(a.KafkaBrokers),
	)
	if err != nil {
		return errors.Wrapf(ctx, err, "create sarama client provider")
	}
	defer saramaClientProvider.Close()

	resultWriter := result.NewResultWriter(gitClient, a.TaskDir, currentDateTime)
	commandConsumer := factory.CreateCommandConsumer(
		saramaClientProvider,
		syncProducer,
		db,
		a.Branch,
		resultWriter,
		gitClient,
		a.TaskDir,
		a.VaultName,
		currentDateTime,
		prCommenter,
	)

	return service.Run(
		ctx,
		syncLoop.Run,
		commandConsumer,
		a.createHTTPServer(syncLoop, restClient),
	)
}

func (a *application) createHTTPServer(
	syncLoop pkgsync.SyncLoop,
	gitRestClient gitrestclient.GitRestClient,
) run.Func {
	return func(ctx context.Context) error {
		router := mux.NewRouter()
		router.Path("/healthz").Handler(libhttp.NewPrintHandler("OK"))
		router.Path("/readiness").HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			ready, err := gitRestClient.IsReady(req.Context())
			if err != nil || !ready {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("git-rest not ready"))
				return
			}
			_, _ = w.Write([]byte("OK"))
		})
		router.Path("/metrics").Handler(promhttp.Handler())
		router.Path("/setloglevel/{level}").
			Handler(log.NewSetLoglevelHandler(ctx, log.NewLogLevelSetter(2, 5*time.Minute)))
		router.Path("/trigger").HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			syncLoop.Trigger()
			glog.V(2).Infof("trigger fired via HTTP")
			_, _ = resp.Write([]byte("trigger fired"))
		})

		glog.V(2).Infof("starting http server listen on %s", a.Listen)
		return libhttp.NewServer(
			a.Listen,
			router,
		).Run(ctx)
	}
}
