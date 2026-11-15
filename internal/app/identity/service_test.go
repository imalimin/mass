package identity

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/storage"
)

func TestCursorScopes(t *testing.T) {
	svc, err := New(nil, nil, time.Hour, []byte("test"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{uuid.NewString(), storage.NewShortID()} {
		cursor := svc.Cursor("actor:space:models", id)
		q, err := svc.Query("actor:space:models", cursor, 20)
		if err != nil || q.After != id || q.Limit != 21 {
			t.Fatal(q, err)
		}
		for _, scope := range []string{"other:space:models", "actor:other:models", "actor:space:credentials"} {
			if _, err = svc.Query(scope, cursor, 20); err == nil {
				t.Fatal("cursor scope bypass")
			}
		}
		for _, c := range []string{cursor + "x", "invalid"} {
			if _, err = svc.Query("actor:space:models", c, 20); err == nil {
				t.Fatal("invalid cursor")
			}
		}
	}
}
