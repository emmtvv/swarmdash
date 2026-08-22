package admin

import (
	"context"
	"time"
)

const gitOpsPollTick = 30 * time.Second

// runGitOpsPoller periodically re-syncs every configured GitStack whose
// PollSeconds > 0 (0 means manual "Sync now" only). Last-checked times are
// kept in memory only - same "resets on restart, that's fine" reasoning as
// pollerState in poller.go, a missed tick after a restart just means the
// next check happens up to gitOpsPollTick later than scheduled, not that a
// deploy is skipped.
func (s *Server) runGitOpsPoller(ctx context.Context) {
	lastChecked := map[string]time.Time{}
	ticker := time.NewTicker(gitOpsPollTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.gitOpsPollOnce(ctx, lastChecked)
		}
	}
}

func (s *Server) gitOpsPollOnce(ctx context.Context, lastChecked map[string]time.Time) {
	stacks, err := s.store.ListGitStacks()
	if err != nil {
		s.log.Warn("gitops poller: list stacks", "err", err)
		return
	}

	seen := make(map[string]bool, len(stacks))
	now := time.Now()
	for _, gs := range stacks {
		seen[gs.ID] = true
		if gs.PollSeconds <= 0 {
			continue
		}
		if last, ok := lastChecked[gs.ID]; ok && now.Sub(last) < time.Duration(gs.PollSeconds)*time.Second {
			continue
		}
		lastChecked[gs.ID] = now
		if err := s.syncGitStack(ctx, gs); err != nil {
			s.log.Warn("gitops poller: sync failed", "stack", gs.StackName, "err", err)
		}
	}

	for id := range lastChecked {
		if !seen[id] {
			delete(lastChecked, id)
		}
	}
}
