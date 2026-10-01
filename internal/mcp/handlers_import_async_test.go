package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/oisee/vibing-steampunk/pkg/saprfc"
)

func asyncResult(t *testing.T, s *Server, args map[string]any) AsyncTask {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := s.handleGetAsyncResult(context.Background(), req)
	if err != nil || res == nil || res.IsError {
		t.Fatalf("GET_ASYNC_RESULT: %v %+v", err, res)
	}
	var task AsyncTask
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &task); err != nil {
		t.Fatalf("decoding the task: %v", err)
	}
	return task
}

// An import started without waiting returns at once; waiting on the task
// reports "running" while tp works -- not an error -- and the outcome after.
func TestStartImport_ReturnsAtOnceAndReportsTheOutcomeLater(t *testing.T) {
	s := &Server{asyncTasks: map[string]*AsyncTask{}}
	release := make(chan struct{})
	run := func(ctx context.Context) (*saprfc.ImportResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &saprfc.ImportResult{System: "PRD", Client: "100", RetCode: "000", Imported: true}, nil
	}

	started := s.startImport(run, []string{"TR-EXAMPLE"}, "100", time.Minute)
	id, _ := started["task_id"].(string)
	if id == "" || started["status"] != "started" {
		t.Fatalf("start = %+v", started)
	}

	if task := asyncResult(t, s, map[string]any{"task_id": id, "wait_seconds": 0.2}); task.Status != "running" {
		t.Errorf("while tp works: status %q, want running", task.Status)
	}

	close(release)
	task := asyncResult(t, s, map[string]any{"task_id": id, "wait_seconds": 5.0})
	if task.Status != "completed" || task.EndedAt == nil {
		t.Fatalf("after the import: %+v", task)
	}
	res, _ := json.Marshal(task.Result)
	if string(res) == "null" {
		t.Error("the import result is missing from the task")
	}
}

func TestStartImport_AFailedImportIsAnErrorOnTheTask(t *testing.T) {
	s := &Server{asyncTasks: map[string]*AsyncTask{}}
	run := func(context.Context) (*saprfc.ImportResult, error) {
		return nil, errors.New("import into PRD client 100 failed: retcode 012")
	}
	id := s.startImport(run, []string{"TR-EXAMPLE"}, "100", time.Minute)["task_id"].(string)
	task := asyncResult(t, s, map[string]any{"task_id": id, "wait_seconds": 5.0})
	if task.Status != "error" || task.Error == "" {
		t.Errorf("task = %+v, want status error with the message", task)
	}
}

// An import whose call was lost after submission is not an error to retry:
// the task says unknown, with the result that names the requests.
func TestStartImport_ALostImportIsUnknownOnTheTask(t *testing.T) {
	s := &Server{asyncTasks: map[string]*AsyncTask{}}
	run := func(context.Context) (*saprfc.ImportResult, error) {
		return &saprfc.ImportResult{System: "PRD", Client: "100", Outcome: saprfc.OutcomeUnknown,
				Requests: []saprfc.ImportedRequest{{Request: "TR-EXAMPLE"}}},
			&saprfc.ImportOutcomeUnknownError{System: "PRD", Client: "100", Requests: []string{"TR-EXAMPLE"},
				Cause: context.DeadlineExceeded}
	}
	id := s.startImport(run, []string{"TR-EXAMPLE"}, "100", time.Minute)["task_id"].(string)
	task := asyncResult(t, s, map[string]any{"task_id": id, "wait_seconds": 5.0})
	if task.Status != saprfc.OutcomeUnknown || task.Error == "" {
		t.Errorf("task = %+v, want status unknown with the message", task)
	}
	res, _ := json.Marshal(task.Result)
	if !strings.Contains(string(res), "TR-EXAMPLE") || !strings.Contains(string(res), `"outcome":"unknown"`) {
		t.Errorf("task result = %s, want the request and outcome unknown", res)
	}
}
