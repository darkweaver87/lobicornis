package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/go-github/v74/github"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/traefik/lobicornis/v3/pkg/conf"
	"github.com/traefik/lobicornis/v3/pkg/json"
	"github.com/traefik/lobicornis/v3/pkg/locker"
	"github.com/traefik/lobicornis/v3/pkg/search"
)

const (
	ghActionLabeled     = "labeled"
	ghActionUnlabeled   = "unlabeled"
	ghActionClosed      = "closed"
	ghWorkflowCompleted = "completed"

	prStatusOpened = "opened"
	prStatusClosed = "closed"
)

type event struct {
	eventType          string
	repository         string
	repositoryFullName string
	pr                 int
	preCheck           func(cfg conf.Configuration, logger zerolog.Logger, ev *event) error
	postRun            func(cfg conf.Configuration, logger zerolog.Logger, ev *event) error
	prStatus           string
}

func processWebhook(cfg conf.Configuration, l locker.Locker, rw http.ResponseWriter, req *http.Request) {
	webhookEvent, err := parseWebhookRequest(req, []byte(os.Getenv(cfg.Server.Webhook.SecretEnvVar)))
	if err != nil {
		log.Error().Err(err).Msg("parse webhook request")
		json.JSONInternalServerError(rw, "parse payload: %s", err)
		return
	}

	var ev *event
	switch e := webhookEvent.(type) {
	case *github.PingEvent:
		ev.eventType = "ping"
		handlePingEvent(rw)
		return
	case *github.PullRequestEvent:
		ev, err = handlePullRequestEvent(cfg, e)
	case *github.WorkflowRunEvent:
		ev, err = handleWorkflowRunEvent(cfg, e)
	default:
		ev = &event{}
		err = fmt.Errorf("unknown event")
	}

	logger := log.With().Str("event", ev.eventType).Str("repo", ev.repositoryFullName).Int("pr", ev.pr).Logger()

	if err != nil {
		logger.Error().Err(err).Msg("process webhook event")
		json.JSONError(rw, http.StatusOK, err.Error())
		return
	}

	logger.Info().Msg("processing event")

	err = runOnEvent(req.Context(), cfg, logger, l, ev)
	if err != nil {
		logger.Error().Err(err).Msg("run on webhook event")
		json.JSONError(rw, http.StatusOK, err.Error())
		return
	}

	_ = json.JSON(rw, http.StatusOK, "running")
}

func parseWebhookRequest(req *http.Request, secretToken []byte) (any, error) {
	payload, err := github.ValidatePayload(req, secretToken)
	if err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	event, err := github.ParseWebHook(github.WebHookType(req), payload)
	if err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}

	return event, nil
}

func handlePingEvent(rw http.ResponseWriter) {
	if err := json.JSON(rw, http.StatusOK, nil); err != nil {
		log.Error().Err(err).Msg("Unable to write JSON response")
	}
}

func handlePullRequestEvent(cfg conf.Configuration, e *github.PullRequestEvent) (*event, error) {
	ev := event{}
	ev.prStatus = prStatusOpened
	ev.pr = e.GetNumber()
	ev.repository = e.Repo.GetName()
	ev.repositoryFullName = e.Repo.GetFullName()
	ev.eventType = fmt.Sprintf("pull_request/%s", e.GetAction())

	switch e.GetAction() {
	case ghActionLabeled:
		if e.Label.GetName() != cfg.Markers.NeedMerge && e.Label.GetName() != cfg.Markers.MergeInProgress {
			return &ev, fmt.Errorf("ignoring label %s", e.Label.GetName())
		}

		ev.preCheck = func(cfg conf.Configuration, logger zerolog.Logger, ev *event) error {
			// GitHub label will be created only once the main process will return a 200
			// so we need to postpone the run of run function
			logger.Info().Msg("waiting for merge label")
			if err := waitForLabelCreated(cfg, ev.repositoryFullName, ev.pr); err != nil {
				return err
			}

			logger.Info().Msg("finished waiting for merge label")
			return nil
		}

		if e.Label.GetName() == cfg.Markers.MergeInProgress {
			mergeable, err := isMergeable(cfg, ev.repository, ev.pr)
			if err != nil {
				return &ev, fmt.Errorf("ignoring label %s: can't get mergeable state: %w", e.Label.GetName(), err)
			}

			if !mergeable {
				return &ev, fmt.Errorf("ignoring label %s: not in mergeable state", e.Label.GetName())
			}
		}
	case ghActionUnlabeled:
		if e.Label.GetName() != cfg.Markers.NeedHumanMerge {
			return &ev, fmt.Errorf("ignoring label %s", e.Label.GetName())
		}

	case ghActionClosed:
		ev.preCheck = func(cfg conf.Configuration, logger zerolog.Logger, ev *event) error {
			// GitHub will consider the PR has merged only once the main process will return a 200
			// so we need to postpone the run of run function
			logger.Info().Msg("waiting for PR to be merged")
			if err := waitForMergedState(cfg, logger, ev.repository, ev.pr); err != nil {
				return err
			}

			ev.prStatus = prStatusClosed
			ev.postRun = func(cfg conf.Configuration, logger zerolog.Logger, ev *event) error {
				ev.pr = 0
				return run(cfg, *ev)
			}

			logger.Info().Msg("finished waiting PR to be merged")
			return nil
		}

		if !isLabeled(e.PullRequest.Labels, cfg.Markers.NeedMerge) {
			return &ev, fmt.Errorf("ignoring closed action on %d: no merge label found", ev.pr)
		}

	default:
		return &ev, fmt.Errorf("invalid action %s", e.GetAction())
	}

	return &ev, nil
}

func handleWorkflowRunEvent(cfg conf.Configuration, e *github.WorkflowRunEvent) (*event, error) {
	ev := event{}
	ev.prStatus = prStatusOpened
	ev.repository = e.Repo.GetName()
	ev.repositoryFullName = e.Repo.GetFullName()
	ev.eventType = fmt.Sprintf("workflow_run/%s", e.GetWorkflowRun().GetStatus())

	if len(e.GetWorkflowRun().PullRequests) == 0 {
		pr, err := getPRFromWorkflowEvent(cfg, ev.repository, e.GetWorkflowRun().GetHeadSHA())
		if err != nil {
			return &ev, fmt.Errorf("unable to find workflow related PR: %w", err)
		}

		ev.pr = pr
	} else {
		ev.pr = e.GetWorkflowRun().PullRequests[0].GetNumber()
	}

	if e.GetWorkflowRun().GetStatus() != ghWorkflowCompleted {
		return &ev, fmt.Errorf("ignoring workflow status %s", e.GetWorkflowRun().GetStatus())
	}

	return &ev, nil
}

func runOnEvent(ctx context.Context, cfg conf.Configuration, logger zerolog.Logger, l locker.Locker, ev *event) error {
	logger.Info().Msg("trying to obtain lock")

	lock, err := l.Obtain(ctx, ev.repository, time.Second)
	if err != nil {
		return fmt.Errorf("repository locked")
	}

	go func() {
		logger.Info().Msg("locked obtained, processing")
		if ev.preCheck != nil {
			err := ev.preCheck(cfg, logger, ev)
			if err != nil {
				logger.Error().Err(err).Msg("unable to run pre check")
			}
		}

		if err == nil {
			if ev.prStatus == prStatusClosed {
				ev.pr = 0 // otherwize event will be ignored later
			}
			err = run(cfg, *ev)
			if err != nil {
				logger.Error().Err(err).Msg("run error")
			}

			if ev.postRun != nil {
				err := ev.postRun(cfg, logger, ev)
				if err != nil {
					logger.Error().Err(err).Msg("unable to run post run")
				}
			}
		}

		// in all cases, release the lock
		err = lock.Release(context.Background())
		if err != nil {
			logger.Error().Err(err).Msg("unable to release the lock")
		} else {
			logger.Info().Msg("run finished and lock released")
		}
	}()

	return nil
}

// waitForLabelCreated waits for NeedMerge label to be created by Github.
func waitForLabelCreated(cfg conf.Configuration, repo string, prNum int) error {
	ctx := context.Background()
	client := newGitHubClient(ctx, cfg.Github.Token, cfg.Github.URL)
	finder := search.New(client, cfg.Markers, cfg.Retry)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			res, err := finder.Search(ctx, cfg.Github.User,
				search.WithLabels(cfg.Markers.NeedMerge),
				search.WithRepository(repo))
			if err != nil {
				log.Error().Err(err).Msg("unable to search")
			}

			for _, repIssue := range res {
				for _, pr := range repIssue {
					if pr.GetNumber() == prNum && isLabeled(pr.Labels, cfg.Markers.NeedMerge) {
						ticker.Stop()
						return nil
					}
				}
			}
		}
	}
}

// waitForMergedState waits for pull request to be merged.
func waitForMergedState(cfg conf.Configuration, logger zerolog.Logger, repo string, prNum int) error {
	ctx := context.Background()
	client := newGitHubClient(ctx, cfg.Github.Token, cfg.Github.URL)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pr, _, err := client.PullRequests.Get(ctx, cfg.Github.User, repo, prNum)
			if err != nil {
				logger.Error().Err(err).Msg("unable to get PR")
			}

			if pr.GetMerged() {
				ticker.Stop()
				return nil
			}
		}
	}
}

// isLabeled checks for the presence of the given label on a PR.
func isLabeled(labels []*github.Label, label string) bool {
	for _, l := range labels {
		if l.GetName() == label {
			return true
		}
	}

	return false
}

// isMergeable checks if a PR is in mergeable state
func isMergeable(cfg conf.Configuration, repo string, prNum int) (bool, error) {
	ctx := context.Background()
	client := newGitHubClient(ctx, cfg.Github.Token, cfg.Github.URL)

	pr, _, err := client.PullRequests.Get(ctx, cfg.Github.User, repo, prNum)
	if err != nil {
		return false, err
	}

	return pr.GetMergeable(), nil
}

// getPRFromWorkflowEvent returns the PR number from a commit SHA. Its needed for PR from forks.
func getPRFromWorkflowEvent(cfg conf.Configuration, repo string, sha string) (int, error) {
	ctx := context.Background()
	client := newGitHubClient(ctx, cfg.Github.Token, cfg.Github.URL)

	pr, _, err := client.PullRequests.ListPullRequestsWithCommit(ctx, cfg.Github.User, repo, sha, &github.ListOptions{PerPage: 10})
	if err != nil {
		return 0, err
	}

	if len(pr) == 0 {
		return 0, fmt.Errorf("no pull request found for %s/%s", repo, sha)
	}

	return pr[0].GetNumber(), nil
}
