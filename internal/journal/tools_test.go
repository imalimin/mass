package journal

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
)

func TestLegacyAndReferencedToolArguments(t *testing.T) {
	args := `{"language":"javascript","code":"return 1;","input":null}`
	d := ResponseData{AttemptID: uuid.NewString(), MessageID: uuid.NewString(), ContentID: uuid.NewString(), FinishReason: "tool_calls", Calls: []run.Call{{ID: uuid.NewString(), Name: "run_code", Arguments: args}}}
	e := New(uuid.NewString(), "model.response.completed", uuid.NewString(), d)
	if err := Validate(e); err != nil {
		t.Fatal("D04 inline arguments rejected", err)
	}
	d.Calls[0].ArgumentsSHA256 = Digest(args)
	d.Calls[0].Arguments = ""
	e.Data, _ = json.Marshal(d)
	if err := Validate(e); err != nil {
		t.Fatal("referenced arguments rejected", err)
	}
	d.Calls[0].Arguments = args + "broken"
	e.Data, _ = json.Marshal(d)
	if Validate(e) == nil {
		t.Fatal("invalid inline arguments accepted")
	}
	d.Calls[0].Arguments = args
	d.Calls[0].ArgumentsSHA256 = Digest("different")
	e.Data, _ = json.Marshal(d)
	if Validate(e) == nil {
		t.Fatal("mismatched arguments digest accepted")
	}
}
