package telegram

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

func (s *taskScheduler) run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("tasks: create watcher: %w", err)
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("tasks: watch directory: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	jobs := make(chan scheduledTask, 1)
	completed := make(chan taskCompletion, 1)
	outgoing := make(chan taskDelivery, 1)
	delivered := make(chan taskDelivery, 1)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case task := <-jobs:
				result := s.bot.checkTask(ctx, task)
				select {
				case completed <- taskCompletion{task, result}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case delivery := <-outgoing:
				if slices.Contains(s.bot.users(), delivery.task.OwnerID) {
					delivery.err = s.bot.send(ctx, delivery.task.ChatID, delivery.text)
				} else {
					delivery.err = errors.New("tasks: delivery skipped: owner access revoked")
				}
				select {
				case delivered <- delivery:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() { cancel(); workers.Wait() }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	fallback := time.NewTicker(time.Minute)
	defer fallback.Stop()
	reloadTimer := time.NewTimer(time.Hour)
	reloadTimer.Stop()
	defer reloadTimer.Stop()
	var reload <-chan time.Time
	var pendingCheck *taskCompletion
	var pendingDelivery *taskDelivery
	checking, sending := false, false
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !s.blocked {
			if pendingCheck != nil {
				if err := s.finish(*pendingCheck); err != nil {
					return err
				}
				pendingCheck = nil
			}
			if pendingDelivery != nil {
				if err := s.delivered(*pendingDelivery); err != nil {
					return err
				}
				pendingDelivery = nil
			}
			if err := s.dispatch(jobs, outgoing, &checking, &sending); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case cmd := <-s.commands:
			reply := s.handleCommand(cmd)
			cmd.reply <- reply
			if reply.err != nil {
				return reply.err
			}
		case result := <-completed:
			checking = false
			pendingCheck = &result
			if err := s.reload(); err != nil {
				s.bot.log(err)
			}
		case result := <-delivered:
			sending = false
			pendingDelivery = &result
			if err := s.reload(); err != nil {
				s.bot.log(err)
			}
		case <-tick.C:
		case <-fallback.C:
			if err := s.reload(); err != nil {
				s.bot.log(err)
			}
		case event, ok := <-watcher.Events:
			if !ok {
				return errors.New("tasks: watcher closed unexpectedly")
			}
			if event.Name == filepath.Dir(s.path) && event.Has(fsnotify.Remove|fsnotify.Rename) {
				return errors.New("tasks: watched directory was removed or renamed")
			}
			if event.Name == s.path && event.Has(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) {
				reloadTimer.Reset(200 * time.Millisecond)
				reload = reloadTimer.C
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return errors.New("tasks: watcher closed unexpectedly")
			}
			return fmt.Errorf("tasks: watcher failed: %w", err)
		case <-reload:
			reload = nil
			if err := s.reload(); err != nil {
				s.bot.log(err)
			}
		}
	}
}

func (s *taskScheduler) dispatch(jobs chan<- scheduledTask, outgoing chan<- taskDelivery, checking, sending *bool) error {
	now := s.now().UTC()
	if !*checking {
		var next *scheduledTask
		for _, task := range s.file.Tasks {
			if task.Enabled && slices.Contains(s.bot.users(), task.OwnerID) && !task.State.NextRun.After(now) &&
				(next == nil || task.State.NextRun.Before(next.State.NextRun)) {
				next = task
			}
		}
		if next != nil {
			// Reconcile editor changes immediately before any state write.
			if err := s.reload(); err != nil {
				s.bot.log(err)
				return nil
			}
			if s.find(next.ID) != next {
				return nil
			}
			check, _, _ := taskIntervals(next.Every, next.ReportEvery)
			missed := int(now.Sub(next.State.NextRun) / check)
			next.State.Counts.Skipped += missed
			next.State.NextRun = now.Add(check - now.Sub(next.State.NextRun)%check)
			next.State.LastStarted, next.State.Running = now, true
			if err := s.save(); err != nil {
				return err
			}
			*checking = true
			jobs <- *next
		}
	}
	if *sending {
		return nil
	}
	for _, task := range s.file.Tasks {
		if !task.Enabled || !slices.Contains(s.bot.users(), task.OwnerID) {
			continue
		}
		if task.State.PendingNotice == "" && (task.State.Running ||
			!task.State.NextRun.After(now) || task.State.NextReport.After(now)) {
			continue
		}
		if err := s.reload(); err != nil {
			s.bot.log(err)
			return nil
		}
		if s.find(task.ID) != task {
			return nil
		}
		delivery := taskDelivery{task: *task, text: task.State.PendingNotice, end: now}
		if delivery.text == "" {
			delivery.report = true
			delivery.text = taskReport(*task, now)
			_, report, _ := taskIntervals(task.Every, task.ReportEvery)
			task.State.NextReport = now.Add(report)
			if err := s.save(); err != nil {
				return err
			}
		}
		*sending = true
		outgoing <- delivery
		return nil
	}
	return nil
}
