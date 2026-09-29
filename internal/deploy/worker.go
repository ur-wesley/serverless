package deploy

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/ur-wesley/oort/internal/store"
)

// MaxBuildLog caps the persisted build log artifact (tail is kept).
const MaxBuildLog = 256 << 10

// Worker executes queued deploy jobs one at a time. Per-function version
// ordering is preserved by the Service's per-function deploy locks.
type Worker struct {
	Svc         *Service
	OnActivated func(fnName string) // e.g. scheduler resync; may be nil
	Poll        time.Duration       // empty-queue sleep (default 1s)

	stop chan struct{}
	done chan struct{}
}

// BuildLogKey is the artifact key for a job's build log.
func BuildLogKey(jobID string) string { return "buildlogs/" + jobID + ".log" }

func (w *Worker) pollInterval() time.Duration {
	if w.Poll > 0 {
		return w.Poll
	}
	return time.Second
}

// Start runs the claim/execute loop until Stop. It requeues jobs left in
// building by a previous crash before taking new work.
func (w *Worker) Start() {
	if n := w.Svc.Store.RequeueStuck(); n > 0 {
		slog.Info("deploy worker requeued stuck jobs", "n", n)
	}
	w.stop = make(chan struct{})
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		for {
			select {
			case <-w.stop:
				return
			default:
			}
			job, ok := w.Svc.Store.ClaimNextJob()
			if !ok {
				select {
				case <-w.stop:
					return
				case <-time.After(w.pollInterval()):
				}
				continue
			}
			if _, err := w.Svc.Execute(context.Background(), job); err != nil {
				slog.Warn("deploy job failed", "job", job.ID, "fn", job.FnName, "err", err)
			}
			if w.OnActivated != nil {
				w.OnActivated(job.FnName)
			}
		}
	}()
}

func (w *Worker) Stop() {
	if w.stop == nil {
		return
	}
	close(w.stop)
	<-w.done
}

// appendLog appends a line to the job's in-artifact build log.
func (s *Service) appendLog(ctx context.Context, job store.Job, line string) {
	key := BuildLogKey(job.ID)
	var cur []byte
	if existing, err := s.Artifacts.Get(ctx, key); err == nil {
		cur = existing
	}
	cur = append(cur, []byte(line+"\n")...)
	if int64(len(cur)) > MaxBuildLog {
		cur = cur[len(cur)-MaxBuildLog:]
		// Cut a partial first line.
		if i := strings.IndexByte(string(cur), '\n'); i >= 0 {
			cur = cur[i+1:]
		}
	}
	_ = s.Artifacts.Put(ctx, key, cur)
	_ = s.Store.SetJobLogKey(job.ID, key)
}
