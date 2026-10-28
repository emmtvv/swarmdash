package admin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/pkg/stdcopy"

	"swarmdash/internal/httpx"
)

// logLine is one line of a stack's merged log, from the service at index
// svc of the stack's service list.
type logLine struct {
	svc  int
	ts   time.Time
	text string
}

// stackLogSource is one service whose logs feed a stack's merged log.
type stackLogSource struct {
	svc   swarm.Service
	label string // service name without the "<stack>_" prefix, padded
}

// handleStackLogsPage renders the log viewer for a whole stack: every
// service's tasks merged into one stream, each line tagged with the
// service it came from.
func (s *Server) handleStackLogsPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sources, err := s.stackLogSources(r.Context(), name, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	services := make([]string, len(sources))
	for i, src := range sources {
		services[i] = src.svc.Spec.Name
	}
	s.render(w, r, "stack_logs.html", map[string]any{
		"StackName": name,
		"Services":  services,
		"User":      userFromContext(r),
	})
}

// stackLogSources lists the stack's services (only the one named only, if
// set), labelled for the merged log.
func (s *Server) stackLogSources(ctx context.Context, stack, only string) ([]stackLogSource, error) {
	services, err := s.listServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	var sources []stackLogSource
	width := 0
	for _, svc := range services {
		if stackName(svc) != stack || (only != "" && svc.Spec.Name != only) {
			continue
		}
		label := strings.TrimPrefix(svc.Spec.Name, stack+"_")
		width = max(width, len(label))
		sources = append(sources, stackLogSource{svc: svc, label: label})
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("stack %q has no services", stack)
	}
	for i := range sources {
		sources[i].label += strings.Repeat(" ", width-len(sources[i].label))
	}
	return sources, nil
}

// serviceLogLines streams a service's log lines (requested with
// timestamps) until the log ends or ctx is canceled. Docker frames each
// log message separately, so stdout and stderr lines never interleave
// mid-line in the demuxed stream.
func (s *Server) serviceLogLines(ctx context.Context, idx int, svc swarm.Service, opts container.LogsOptions) (<-chan logLine, error) {
	opts.ShowStdout, opts.ShowStderr, opts.Timestamps = true, true, true
	rc, err := s.docker.ServiceLogs(ctx, svc.ID, opts)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, rc)
		rc.Close()
		pw.CloseWithError(err)
	}()
	out := make(chan logLine, 256)
	go func() {
		defer close(out)
		defer pr.Close()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			l := logLine{svc: idx, text: sc.Text()}
			if ts, rest, ok := strings.Cut(l.text, " "); ok {
				if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
					l.ts, l.text = t, rest
				}
			}
			select {
			case out <- l:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// mergeLogLines emits the lines of several finite streams in timestamp
// order, holding just one pending line per stream - each service's log is
// already (roughly) in order, so this needn't buffer a whole log to sort it.
func mergeLogLines(streams []<-chan logLine, emit func(logLine) error) error {
	heads := make([]*logLine, len(streams))
	advance := func(i int) {
		heads[i] = nil
		if l, ok := <-streams[i]; ok {
			heads[i] = &l
		}
	}
	for i := range streams {
		advance(i)
	}
	for {
		next := -1
		for i, h := range heads {
			if h != nil && (next == -1 || h.ts.Before(heads[next].ts)) {
				next = i
			}
		}
		if next == -1 {
			return nil
		}
		if err := emit(*heads[next]); err != nil {
			return err
		}
		advance(next)
	}
}

func formatLogLine(sources []stackLogSource, l logLine) string {
	const layout = "2006-01-02 15:04:05.000"
	ts := "-" + strings.Repeat(" ", len(layout)-1)
	if !l.ts.IsZero() {
		ts = l.ts.UTC().Format(layout)
	}
	return ts + " " + sources[l.svc].label + " | " + l.text + "\n"
}

// writeStackHistory writes the stack's existing logs (the last tail lines
// of each service, or all of them) merged in timestamp order, and returns
// the timestamp of the last line written per service.
func (s *Server) writeStackHistory(ctx context.Context, w io.Writer, sources []stackLogSource, tail string) ([]time.Time, error) {
	streams := make([]<-chan logLine, 0, len(sources))
	for i, src := range sources {
		ch, err := s.serviceLogLines(ctx, i, src.svc, container.LogsOptions{Tail: tail})
		if err != nil {
			fmt.Fprintf(w, "[swarmdash] %s: %v\n", src.svc.Spec.Name, err)
			continue
		}
		streams = append(streams, ch)
	}
	last := make([]time.Time, len(sources))
	err := mergeLogLines(streams, func(l logLine) error {
		if l.ts.After(last[l.svc]) {
			last[l.svc] = l.ts
		}
		_, err := io.WriteString(w, formatLogLine(sources, l))
		return err
	})
	return last, err
}

// handleStackLogsProxy streams a stack's merged logs over a websocket:
// first the recent history of every service, merged by timestamp, then
// (with follow) new lines as they arrive. Like a service's logs this goes
// through the swarm manager's ServiceLogs, not per-node agents. Query
// params: tail (per service, default 200), follow (default true) and
// service to show just one of the stack's services.
func (s *Server) handleStackLogsProxy(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sources, err := s.stackLogSources(r.Context(), r.PathValue("name"), q.Get("service"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	tail := q.Get("tail")
	if tail == "" {
		tail = "200"
	}
	follow := q.Get("follow") != "false"

	conn, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	// The request context isn't canceled when a hijacked connection goes
	// away, so watch for the browser closing the socket ourselves.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	out := bufio.NewWriterSize(&wsTextWriter{conn: conn}, 32*1024)
	started := time.Now()
	last, err := s.writeStackHistory(ctx, out, sources, tail)
	if err != nil || out.Flush() != nil || !follow {
		return
	}

	// Follow from just after each service's last history line, so nothing
	// logged in between is lost or shown twice.
	live := make(chan logLine, 256)
	for i, src := range sources {
		since := started
		if !last[i].IsZero() {
			since = last[i].Add(time.Nanosecond)
		}
		ch, err := s.serviceLogLines(ctx, i, src.svc, container.LogsOptions{Follow: true, Since: since.Format(time.RFC3339Nano)})
		if err != nil {
			fmt.Fprintf(out, "[swarmdash] %s: %v\n", src.svc.Spec.Name, err)
			continue
		}
		go func() {
			for l := range ch {
				select {
				case live <- l:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case l := <-live:
			if _, err := out.WriteString(formatLogLine(sources, l)); err != nil {
				return
			}
			if len(live) == 0 && out.Flush() != nil {
				return
			}
		}
	}
}

// handleStackLogsDownload returns a stack's complete merged logs as a
// plain HTTP attachment (never follows).
func (s *Server) handleStackLogsDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sources, err := s.stackLogSources(r.Context(), name, r.URL.Query().Get("service"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	httpx.SetAttachment(w, name+".log")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = s.writeStackHistory(r.Context(), w, sources, "all")
}
