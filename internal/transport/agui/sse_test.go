package agui

import (
	"bytes"
	"testing"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
)

func TestCursorAndSSE(t *testing.T) {
	for _, value := range []string{"", "-1", "+1", "01", "1.0", "9223372036854775808"} {
		if _, err := ParseCursor(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if n, err := ParseCursor("9223372036854775807"); err != nil || n != 9223372036854775807 {
		t.Fatalf("bigint: %d %v", n, err)
	}
	var b bytes.Buffer
	if err := EncodeFrame(&b, "1", events.NewRunStartedEvent("s", "r")); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b.Bytes(), []byte("id: 1\ndata: {")) || !bytes.HasSuffix(b.Bytes(), []byte("\n\n")) {
		t.Fatalf("invalid frame %q", b.String())
	}
	if err := EncodeFrame(&b, "0", events.NewRunStartedEvent("s", "r")); err == nil {
		t.Fatal("event 0 accepted")
	}
	if err := EncodeFrame(&b, "2", events.NewRunStartedEvent("", "r")); err == nil {
		t.Fatal("invalid SDK event accepted")
	}
}
