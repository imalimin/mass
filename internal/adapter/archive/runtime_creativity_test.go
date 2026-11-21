package archive

import (
	"encoding/json"
	"testing"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource/sessionlog"
)

func TestRehydrateCreativitySession(t *testing.T) {
	for _, test := range []struct {
		name       string
		creativity *run.Creativity
	}{
		{name: "ordinary"},
		{name: "creative", creativity: &run.Creativity{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			created := sessionlog.Created{
				Header:        sessionlog.Header{Seq: 1, Type: sessionlog.SessionCreated, Role: "system"},
				SchemaVersion: 1, SessionID: storage.NewShortID(), SpaceID: storage.NewShortID(),
				Creativity: test.creativity,
			}
			record, err := json.Marshal(created)
			if err != nil {
				t.Fatal(err)
			}
			restored := newConversation()
			if err = (&Runtime{}).rehydrate(restored, []json.RawMessage{record}); err != nil {
				t.Fatal(err)
			}
			var data journal.SessionData
			if err = journal.Decode(restored.events[0].Data, &data); err != nil {
				t.Fatal(err)
			}
			if (restored.session.Creativity == nil) != (test.creativity == nil) || (data.Creativity == nil) != (test.creativity == nil) {
				t.Fatalf("unexpected restored creativity: session=%v event=%v", restored.session.Creativity, data.Creativity)
			}
		})
	}
}
