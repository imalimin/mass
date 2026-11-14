package trace

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/tests/fakes"
)

func TestSessionArchiveRetriesAndOverwrites(t *testing.T) {
	ctx := context.Background()
	blobs := fakes.NewBlobs()
	recorder := &Recorder{}
	archive := &Archive{Root: t.TempDir(), Prefix: "test", Blobs: blobs, Recorder: recorder}
	started := time.Now().UTC()
	finished := started.Add(time.Second)
	s := run.Snapshot{SpaceID: uuid.NewString(), SessionID: uuid.NewString(), ID: uuid.NewString()}
	first := run.View{Snapshot: s, Status: "failed", StartedAt: &started, FinishedAt: &finished, Error: "model_failed"}
	one, two := uuid.NewString(), uuid.NewString()
	recorder.Start(s, one, "", "task", started)
	recorder.ModelDone(s.ID, run.Reply{}, "failed", "model_http_failed", 100*time.Millisecond)
	recorder.Start(s, two, one, "task", started)
	reasoning, reasoningTokens := "inspect", int64(7)
	recorder.ModelDone(s.ID, run.Reply{
		Message: run.Message{Content: "partial"},
		Trace: run.ModelTrace{Reasoning: &reasoning, UsagePresent: true, ReasoningTokens: &reasoningTokens, ToolCalls: []run.NativeToolCall{{
			ID: "provider-id", Type: "function", Function: run.NativeFunctionCall{Name: "read_file", Arguments: `{"path":`},
		}}},
	}, "failed", "model_interrupted", time.Second)

	blobs.SetFailure(true)
	if err := archive.Save(ctx, first, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := archive.Upload(ctx, first.Snapshot); err == nil {
		t.Fatal("expected TOS outage")
	}
	// A new process can retry the upload from the existing local file.
	archive.Recorder = &Recorder{}
	if err := archive.Save(ctx, first, nil, false); err != nil {
		t.Fatal(err)
	}
	blobs.SetFailure(false)
	if err := archive.Upload(ctx, first.Snapshot); err != nil {
		t.Fatal(err)
	}

	second := run.View{Snapshot: s, Status: "cancelled", FinishedAt: &finished, Error: "run_cancelled"}
	second.ID = uuid.NewString()
	path, key := archive.locations(s)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := archive.Save(ctx, second, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := archive.Upload(ctx, second.Snapshot); err != nil {
		t.Fatal(err)
	}
	remote, err := blobs.Open(ctx, key, "")
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	var lines []lineHeader
	err = readLines(ctx, remote, func(_ []byte, line lineHeader) error {
		lines = append(lines, line)
		return nil
	})
	if err != nil || len(lines) != 4 || lines[2] != (lineHeader{RunID: first.ID, Type: "run_finished"}) || lines[3] != (lineHeader{RunID: second.ID, Type: "run_finished"}) {
		t.Fatal("expected one overwritten session file with each run exactly once", lines, err)
	}

	first.TraceUploaded = true
	page, err := archive.Read(ctx, first, 0, nil, 1)
	if err != nil || !page.HasMore || page.Through != 3 || page.UploadStatus != "uploaded" {
		t.Fatal("first page", page, err)
	}
	var attempt ModelStep
	if err := json.Unmarshal(page.Items[0], &attempt); err != nil || attempt.Model.Usage != nil {
		t.Fatal("absent usage should be null", attempt.Model.Usage, err)
	}
	page, err = archive.Read(ctx, first, page.Next, &page.Through, 1)
	if err != nil || len(page.Items) != 1 || json.Unmarshal(page.Items[0], &attempt) != nil {
		t.Fatal("retry page", page, err)
	}
	if attempt.Step != 1 || attempt.RetryOf != one || attempt.Model.Usage == nil || *attempt.Model.Usage != (Usage{ReasoningTokens: 7}) ||
		attempt.Model.Reasoning == nil || *attempt.Model.Reasoning != reasoning || len(attempt.Model.ToolCalls) != 1 || attempt.Model.ToolCalls[0].Function.Arguments != `{"path":` {
		t.Fatalf("provider trace not preserved: %+v", attempt)
	}
	second.TraceUploaded = true
	page, err = archive.Read(ctx, second, 0, nil, 1)
	var end RunFinished
	if err != nil || len(page.Items) != 1 || json.Unmarshal(page.Items[0], &end) != nil || end.Status != "cancelled" || end.StartedAt != nil || end.DurationMS != nil {
		t.Fatalf("queued cancellation: %+v, %v", end, err)
	}
	if _, err := archive.Read(ctx, first, 2, new(int64), 1); err == nil {
		t.Fatal("accepted a backwards page")
	}
}
