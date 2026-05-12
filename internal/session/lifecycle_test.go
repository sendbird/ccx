package session

import (
	"testing"
	"time"
)

func TestLifecycle(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		sess Session
		want LifecycleState
	}{
		{
			name: "responding wins over everything",
			sess: Session{
				IsLive:       true,
				IsResponding: true,
				HasShellJobs: true,
				Todos:        []TodoItem{{Status: "pending"}},
			},
			want: LifecycleBusy,
		},
		{
			name: "live shell job → BG",
			sess: Session{
				IsLive:       true,
				HasShellJobs: true,
				ModTime:      now,
			},
			want: LifecycleBG,
		},
		{
			name: "active cron → BG even when not live",
			sess: Session{
				Crons:   []CronItem{{Status: "active"}},
				ModTime: now,
			},
			want: LifecycleBG,
		},
		{
			name: "deleted cron is not BG",
			sess: Session{
				Crons:   []CronItem{{Status: "deleted"}},
				ModTime: now,
			},
			want: LifecycleNone,
		},
		{
			name: "shell jobs on a dead session are ignored",
			sess: Session{
				HasShellJobs: true,
				ModTime:      now,
			},
			want: LifecycleNone,
		},
		{
			name: "live + stale + unfinished → STUCK",
			sess: Session{
				IsLive:  true,
				ModTime: now.Add(-45 * time.Minute),
				Todos:   []TodoItem{{Status: "in_progress"}},
			},
			want: LifecycleStuck,
		},
		{
			name: "live + stale but no unfinished work → not stuck",
			sess: Session{
				IsLive:  true,
				ModTime: now.Add(-45 * time.Minute),
				Todos:   []TodoItem{{Status: "completed"}},
			},
			want: LifecycleDone,
		},
		{
			name: "live + fresh + unfinished → WAIT",
			sess: Session{
				IsLive:  true,
				ModTime: now,
				Tasks:   []TaskItem{{Status: "pending"}},
			},
			want: LifecycleWait,
		},
		{
			name: "live + nothing pending → none",
			sess: Session{
				IsLive:  true,
				ModTime: now,
			},
			want: LifecycleNone,
		},
		{
			name: "non-live + all completed todos → DONE",
			sess: Session{
				ModTime: now.Add(-2 * time.Hour),
				Todos:   []TodoItem{{Status: "completed"}, {Status: "completed"}},
			},
			want: LifecycleDone,
		},
		{
			name: "non-live + mixed todos → none (no signal we trust)",
			sess: Session{
				ModTime: now.Add(-2 * time.Hour),
				Todos:   []TodoItem{{Status: "completed"}, {Status: "pending"}},
			},
			want: LifecycleNone,
		},
		{
			name: "empty session → none",
			sess: Session{ModTime: now},
			want: LifecycleNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sess.Lifecycle(); got != tc.want {
				t.Errorf("Lifecycle() = %v, want %v", got, tc.want)
			}
		})
	}
}
