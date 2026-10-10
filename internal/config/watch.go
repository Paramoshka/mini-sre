package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Live publishes immutable configurations. Callers must not mutate Current's maps or slices.
type Live struct {
	current atomic.Pointer[Config]
}

func (l *Live) Current() Config {
	return *l.current.Load()
}

func Watch(ctx context.Context, path string, telegram bool, errOut io.Writer, fatal func(error)) (*Live, error) {
	var watcher *fsnotify.Watcher
	if path != "" {
		var err error
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("config: resolve file: %w", err)
		}
		watcher, err = fsnotify.NewWatcher()
		if err != nil {
			return nil, fmt.Errorf("config: create watcher: %w", err)
		}
		if err := watcher.Add(filepath.Dir(path)); err != nil {
			watcher.Close()
			return nil, fmt.Errorf("config: watch directory: %w", err)
		}
	}
	cfg, err := loadForMode(path, telegram)
	if err != nil {
		if watcher != nil {
			watcher.Close()
		}
		return nil, err
	}
	live := &Live{}
	live.current.Store(&cfg)
	if watcher != nil {
		go func() {
			if err := live.watch(ctx, watcher, path, telegram, errOut); err != nil {
				fatal(err)
			}
		}()
	}
	return live, nil
}

func loadForMode(path string, telegram bool) (Config, error) {
	cfg, err := Load(path)
	if err != nil {
		return Config{}, err
	}
	if telegram && len(cfg.Telegram.AllowedUserIDs) == 0 {
		return Config{}, errors.New("config: telegram.allowed_user_ids must not be empty")
	}
	return cfg, nil
}

func (l *Live) watch(ctx context.Context, watcher *fsnotify.Watcher, path string, telegram bool, errOut io.Writer) error {
	defer watcher.Close()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var reload <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("config: watcher closed unexpectedly")
			}
			if event.Name == filepath.Dir(path) && event.Has(fsnotify.Remove|fsnotify.Rename) {
				return errors.New("config: watched directory was removed or renamed")
			}
			if event.Name == path && event.Has(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) {
				timer.Reset(200 * time.Millisecond)
				reload = timer.C
			}
		case err, ok := <-watcher.Errors:
			if ctx.Err() != nil {
				return nil
			}
			if !ok {
				return errors.New("config: watcher closed unexpectedly")
			}
			return fmt.Errorf("config: watcher failed: %w", err)
		case <-reload:
			reload = nil
			cfg, err := loadForMode(path, telegram)
			if err != nil {
				if errOut != nil {
					fmt.Fprintln(errOut, "config: reload rejected:", err)
				}
				continue
			}
			l.current.Store(&cfg)
			if errOut != nil {
				fmt.Fprintln(errOut, "config: reloaded")
			}
		}
	}
}
