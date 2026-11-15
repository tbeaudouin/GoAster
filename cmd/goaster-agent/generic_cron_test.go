package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"goaster/internal/attachment"
	"goaster/internal/chat"
	"goaster/internal/scheduler"
)

type genericCronRegistrar struct {
	tools chan chat.Tool
}

func (registrar genericCronRegistrar) RegisterTool(tool chat.Tool) error {
	registrar.tools <- tool
	return nil
}

type genericCronSession struct {
	replyScheduled func(context.Context, string) (string, error)
}

func (session genericCronSession) Reply(ctx context.Context, message string) (string, error) {
	return session.replyScheduled(ctx, message)
}

func (session genericCronSession) ReplyScheduled(ctx context.Context, message string) (string, error) {
	return session.replyScheduled(ctx, message)
}

func (session genericCronSession) ReplyWithAttachments(ctx context.Context, message string, _ []attachment.Metadata) (string, error) {
	return session.replyScheduled(ctx, message)
}

func (genericCronSession) PendingModelRecoveryNotice() string { return "" }

func genericCronConversation(chatID, threadID int64) context.Context {
	ctx := chat.WithConversationType(context.Background(), "supergroup")
	ctx = chat.WithConversationID(ctx, chatID)
	return chat.WithConversationThreadID(ctx, threadID)
}

func TestGenericCronProductionLifecyclePersistsExecutionAndDelivery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	entered := make(chan struct{})
	received := make(chan struct {
		chatID   int64
		threadID int64
		message  string
	}, 1)
	session := genericCronSession{replyScheduled: func(ctx context.Context, message string) (string, error) {
		chatID, _ := chat.ConversationID(ctx)
		threadID, _ := chat.ConversationThreadID(ctx)
		received <- struct {
			chatID   int64
			threadID int64
			message  string
		}{chatID: chatID, threadID: threadID, message: message}
		close(entered)
		return "scheduled reply", nil
	}}
	sessionFor := newAgentSessionFactory(func(int64) agentSession { return session })
	var deliveryQueue cronDeliveryQueue
	jobs, err := newCronScheduler(sessionFor, func(job scheduler.Job, reply string) error {
		return deliveryQueue.Enqueue(job, reply)
	})
	if err != nil {
		t.Fatal(err)
	}
	deliveryQueue, err = newCronDeliveryQueue(filepath.Join(filepath.Dir(jobs.Path()), "deliveries.json"))
	if err != nil {
		t.Fatal(err)
	}
	registrar := genericCronRegistrar{tools: make(chan chat.Tool, 1)}
	if err := chat.RegisterCronTool(registrar, jobs); err != nil {
		t.Fatal(err)
	}
	tool := <-registrar.tools
	if tool.Name != "cron" || tool.Handler == nil {
		t.Fatalf("production cron registration = %+v", tool)
	}
	result, err := tool.Handler(genericCronConversation(700, 33), `{"action":"add","every_seconds":1,"message":"scheduled message","max_runs":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatal("cron add returned an empty result")
	}
	if err := jobs.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		jobs.Stop()
		t.Fatal("generic cron job did not execute")
	}
	jobs.Stop()
	request := <-received
	if request.message != "scheduled message" || request.chatID != 700 || request.threadID != 33 {
		t.Fatalf("scheduled request = %+v", request)
	}
	deliveries, err := deliveryQueue.Pending()
	if err != nil || len(deliveries) != 1 || deliveries[0].ChatID != 700 || deliveries[0].ThreadID != 33 || deliveries[0].Text != "scheduled reply" {
		t.Fatalf("scheduled deliveries = %+v, %v; jobs = %+v", deliveries, err, jobs.List())
	}
	restarted, err := scheduler.New(scheduler.Options{Path: jobs.Path()})
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := restarted.Get(mustCronJobID(t, result))
	if !ok || saved.Status != scheduler.StatusCompleted || saved.RunCount != 1 || saved.Enabled || saved.OriginChatID != 700 || saved.OriginThreadID != 33 {
		t.Fatalf("persisted generic cron job = %+v", saved)
	}
	restartedQueue, err := newCronDeliveryQueue(filepath.Join(filepath.Dir(jobs.Path()), "deliveries.json"))
	if err != nil {
		t.Fatal(err)
	}
	restartedDeliveries, err := restartedQueue.Pending()
	if err != nil || len(restartedDeliveries) != 1 || restartedDeliveries[0].ID != deliveries[0].ID {
		t.Fatalf("restarted deliveries = %+v, %v", restartedDeliveries, err)
	}
	if err := restartedQueue.Ack([]string{deliveries[0].ID}); err != nil {
		t.Fatal(err)
	}
	if remaining, err := restartedQueue.Pending(); err != nil || len(remaining) != 0 {
		t.Fatalf("acknowledged deliveries = %+v, %v", remaining, err)
	}
}

func mustCronJobID(t *testing.T, result string) string {
	t.Helper()
	var job scheduler.Job
	if err := json.Unmarshal([]byte(result), &job); err != nil {
		t.Fatal(err)
	}
	if job.ID == "" {
		t.Fatal("cron result did not contain a job ID")
	}
	return job.ID
}

func TestGenericCronShutdownRetainsInterruptedExecutionForReview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	entered := make(chan struct{})
	session := genericCronSession{replyScheduled: func(ctx context.Context, _ string) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", scheduler.ReviewRequiredError{Message: "scheduled turn requires review", Cause: ctx.Err()}
	}}
	sessionFor := newAgentSessionFactory(func(int64) agentSession { return session })
	jobs, err := newCronScheduler(sessionFor, nil)
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "interrupt me", OriginChatID: 701, OriginChatType: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		jobs.Stop()
		t.Fatal("generic cron job did not start before shutdown")
	}
	jobs.Stop()
	restarted, err := scheduler.New(scheduler.Options{Path: jobs.Path()})
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := restarted.Get(job.ID)
	if !ok || saved.Enabled || !saved.ReviewRequired || saved.Status != scheduler.StatusFailed || saved.RunCount != 1 {
		t.Fatalf("shutdown state = %+v", saved)
	}
	if saved.Error != "scheduled turn requires review" {
		t.Fatalf("shutdown review error = %q", saved.Error)
	}
}
