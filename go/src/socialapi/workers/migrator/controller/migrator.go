package controller

import (
	"errors"
	"fmt"
	"koding/db/mongodb/modelhelper"
	"socialapi/models"
	webhookmodels "socialapi/workers/integration/webhook"
	realtimemodels "socialapi/workers/realtime/models"

	"github.com/jinzhu/gorm"
	"github.com/koding/logging"
	"github.com/robfig/cron"
)

const SCHEDULE = "0 */5 * * * *"

var (
	ErrMigrated = errors.New("already migrated")
)

type Controller struct {
	log     logging.Logger
	pubnub  *realtimemodels.PubNub
	cronJob *cron.Cron
	ready   chan bool
}

func New(log logging.Logger, pubnub *realtimemodels.PubNub) (*Controller, error) {

	wc := &Controller{
		log:    log,
		pubnub: pubnub,
		ready:  make(chan bool, 1),
	}

	return wc, nil
}

func (mwc *Controller) Schedule() error {
	mwc.cronJob = cron.New()
	mwc.ready <- true
	err := mwc.cronJob.AddFunc(SCHEDULE, mwc.CronStart)
	if err != nil {
		return err
	}
	mwc.cronJob.Start()

	return nil
}

func (mwc *Controller) Shutdown() {
	mwc.cronJob.Stop()
}

func (mwc *Controller) CronStart() {
	select {
	case <-mwc.ready:
		mwc.Start()
	default:
		mwc.log.Debug("Ongoing migration process")
	}
}

func (mwc *Controller) Start() {
	mwc.log.Notice("Migration started")

	mwc.migrateAllAccounts()

	mwc.migrateAllGroups()

	mwc.createPublicChannel()

	mwc.createChangelogChannel()

	mwc.migrateAllAccountsToAlgolia()

	mwc.GrantPublicAccess()

	mwc.CreateIntegrations()

	mwc.CreateBotUser()

	mwc.log.Notice("Migration finished")

	mwc.ready <- true
}

func (mwc *Controller) AccountIdByOldId(oldId string) (int64, error) {
	acc, err := models.Cache.Account.ByOldId(oldId)
	if err == nil {
		return acc.Id, nil
	}

	acc1, err := modelhelper.GetAccountById(oldId)
	if err != nil {
		return 0, fmt.Errorf("Participant account %s cannot be fetched: %s", oldId, err)
	}

	a := models.NewAccount()
	a.OldId = oldId
	a.Nick = acc1.Profile.Nickname
	if err := a.FetchOrCreate(); err != nil {
		return 0, err
	}

	if err := models.Cache.Account.SetToCache(a); err != nil {
		return 0, err
	}

	return a.Id, nil
}

func (mwc *Controller) CreateIntegrations() {
	mwc.log.Notice("Creating integration channels")

	githubInt := webhookmodels.NewIntegration()
	githubInt.Title = "GitHub"
	githubInt.Name = "github"
	githubInt.Summary = "Source control and code management."
	githubInt.IconPath = "https://koding-cdn.s3.amazonaws.com/temp-images/github.png"
	githubInt.Description = "GitHub offers online source code hosting for Git projects, with powerful collaboration, code review, and issue tracking. \n \n This integration will post commits, pull requests, and activity on GitHub Issues to a channel in Koding."
	githubInt.Instructions = `
#### Step 1

In your GitHub account, go to the repository that you'd like to monitor. Click on the **Settings** tab in the right navigation.

![github_step1.png](https://koding-cdn.s3.amazonaws.com/temp-images/airbrake_step1.png)


#### Step 2

Click on **Webhooks & Services** in the left navigation, and then press the **Add webhook** button.

![airbrake_step2.png](https://koding-cdn.s3.amazonaws.com/temp-images/airbrake_step2.png)

`
	githubInt.Settings = gorm.Hstore{}

	events := webhookmodels.NewEvents(
		webhookmodels.NewEvent("push", "Commits pushed to the repository"),
		webhookmodels.NewEvent("commit_comment", "New comment on commit"),
		webhookmodels.NewEvent("pull_request", "Pull request opened or closed"),
		webhookmodels.NewEvent("issues", "Issues opened or closed"),
		webhookmodels.NewEvent("issue_comment", "New comment on issue"),
		webhookmodels.NewEvent("deployment_status", "Show deployment statuses"),
		webhookmodels.NewEvent("create", "Branch or tag created"),
		webhookmodels.NewEvent("delete", "Branch or tag deleted"),
		webhookmodels.NewEvent("pull_request_review_comment", "New comment on pull request"),
	)

	githubInt.AddSettings("authorizable", true)

	githubInt.AddEvents(events)

	if err := githubInt.Create(); err != nil {
		mwc.log.Error("Could not create integration: %s", err)
	}

	pivotalInt := webhookmodels.NewIntegration()
	pivotalInt.Title = "Pivotal Tracker"
	pivotalInt.Name = "pivotal"
	pivotalInt.Summary = "Collaborative, lightweight agile project management."
	pivotalInt.IconPath = "https://koding-cdn.s3.amazonaws.com/temp-images/pivotaltracker.png"
	pivotalInt.Description = "Pivotal Tracker is an agile project management tool that shows software teams their work in progress and allows them to track upcoming milestones. This integration will post updates to a channel in Koding whenever a story activity occurs in Pivotal Tracker."

	if err := pivotalInt.Create(); err != nil {
		mwc.log.Error("Could not create integration: %s", err)
	}

	pagerdutyInt := webhookmodels.NewIntegration()
	pagerdutyInt.Title = "Pagerduty"
	pagerdutyInt.Name = "pagerduty"
	pagerdutyInt.Summary = "On-call scheduling, alerting, and incident tracking."
	pagerdutyInt.IconPath = "https://s3.amazonaws.com/koding-cdn/temp-images/pagerduty.png"
	pagerdutyInt.Description = "PagerDuty provides IT alert monitoring, on-call scheduling, escalation policies and incident tracking to fix problems in your apps, servers and websites."

	pagerdutyInt.Settings = gorm.Hstore{}

	pdEvents := webhookmodels.NewEvents(
		webhookmodels.NewEvent("incident.trigger", "Newly triggered"),
		webhookmodels.NewEvent("incident.acknowledge", "Acknowledged"),
		webhookmodels.NewEvent("incident.resolve", "Resolved"),
		webhookmodels.NewEvent("incident.assign", "Manually reassigned"),
		webhookmodels.NewEvent("incident.escalate", "Escalated"),
		webhookmodels.NewEvent("incident.unacknowledge", "Unacknowledged due to timeout"),
	)

	pagerdutyInt.AddEvents(pdEvents)

	if err := pagerdutyInt.Create(); err != nil {
		mwc.log.Error("Could not create integration: %s", err)
	}

	i := webhookmodels.NewIntegration()
	i.Title = "iterable"
	i.Name = "iterable"
	i.Summary = "Email engagement service"
	i.IsPublished = false

	err := i.Create()
	if err != nil {
		mwc.log.Error("Could not create integration: %s", err)
		return
	}

	ch := models.NewChannel()
	if err := ch.FetchPublicChannel("koding"); err != nil {
		mwc.log.Error("Could not fetch koding channel: %s", err)
		return
	}

	acc := models.NewAccount()

	if err := acc.ByNick("devrim"); err != nil {
		mwc.log.Error("Could not fetch account: %s", err)
		return
	}

	ci := webhookmodels.NewChannelIntegration()
	ci.IntegrationId = i.Id
	ci.ChannelId = ch.Id
	ci.CreatorId = acc.Id
	ci.GroupName = "koding"

	if err := ci.Create(); err != nil {
		mwc.log.Error("Could not create channel integration: %s", err)
		return
	}

}

// TO-DO update integrations on-the-fly

func (mwc *Controller) CreateBotUser() {
	mwc.log.Notice("Creating bot user")
	_, err := models.CreateAccountInBothDbsWithNick("bot")
	if err != nil {
		mwc.log.Error("Could not create bot account")
	}
}
