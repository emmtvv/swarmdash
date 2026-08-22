package agent

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
	// The only caller of these endpoints is the admin process, dialing
	// server-to-server, so origin checks add nothing here.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Wire framing for the client(admin)->agent direction of the exec socket.
// The first byte of every binary message is a frame type; everything after
// it is the payload. Agent->admin is always raw output bytes, no framing
// needed since there is only one kind of message in that direction.
const (
	frameStdin  byte = 0
	frameResize byte = 1
)

type resizePayload struct {
	Cols uint `json:"cols"`
	Rows uint `json:"rows"`
}

// handleExec bridges a websocket connection to `docker exec -it <cmd>` on
// the target container. Query params: cmd (default /bin/sh), cols, rows.
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	shell := r.URL.Query().Get("cmd")
	if shell == "" {
		shell = "/bin/sh"
	}
	cols, rows := parseUintDefault(r.URL.Query().Get("cols"), 80), parseUintDefault(r.URL.Query().Get("rows"), 24)

	ctx := r.Context()
	execCreate, err := s.docker.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{shell},
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		ConsoleSize:  &[2]uint{rows, cols},
	})
	if err != nil {
		http.Error(w, "exec create: "+err.Error(), http.StatusBadGateway)
		return
	}

	hijacked, err := s.docker.ContainerExecAttach(ctx, execCreate.ID, container.ExecAttachOptions{
		Tty:         true,
		ConsoleSize: &[2]uint{rows, cols},
	})
	if err != nil {
		http.Error(w, "exec attach: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer hijacked.Close()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	// agent stdout/stderr (merged, tty) -> browser (via admin)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, err := hijacked.Reader.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// browser (via admin) -> container stdin, plus resize control frames
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt != websocket.BinaryMessage || len(data) == 0 {
			continue
		}
		switch data[0] {
		case frameStdin:
			if _, err := hijacked.Conn.Write(data[1:]); err != nil {
				goto out
			}
		case frameResize:
			var rp resizePayload
			if json.Unmarshal(data[1:], &rp) == nil {
				_ = s.docker.ContainerExecResize(ctx, execCreate.ID, container.ResizeOptions{
					Height: rp.Rows,
					Width:  rp.Cols,
				})
			}
		}
	}
out:
	<-done
}

func parseUintDefault(s string, def uint) uint {
	if s == "" {
		return def
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return def
	}
	return uint(v)
}
