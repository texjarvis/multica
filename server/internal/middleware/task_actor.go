package middleware

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/util"
)

// TaskActor is the server-stamped identity of an mat_ task token.
// Auth overwrites these headers from the token row after deleting any
// client-supplied X-Actor-Source. A missing field fails closed.
type TaskActor struct {
	UserID      string
	AgentID     string
	TaskID      string
	WorkspaceID string
}

// TaskActorFromRequest returns the task-token actor only when every stamped
// identifier is present and a UUID. It does not accept cloud PATs, human
// tokens, or a partial header set.
func TaskActorFromRequest(r *http.Request) (TaskActor, bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		return TaskActor{}, false
	}
	actor := TaskActor{
		UserID:      r.Header.Get("X-User-ID"),
		AgentID:     r.Header.Get("X-Agent-ID"),
		TaskID:      r.Header.Get("X-Task-ID"),
		WorkspaceID: r.Header.Get("X-Workspace-ID"),
	}
	for _, id := range []string{actor.UserID, actor.AgentID, actor.TaskID, actor.WorkspaceID} {
		if _, err := util.ParseUUID(id); err != nil {
			return TaskActor{}, false
		}
	}
	return actor, true
}
