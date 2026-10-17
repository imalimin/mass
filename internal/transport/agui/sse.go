package agui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
)

const Profile = "mass-agui-v1"

// ParseCursor accepts canonical signed-bigint decimal strings without going
// through a JavaScript number. Zero is the initial cursor, never an event ID.
func ParseCursor(cursor string) (int64, error) {
	n, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != cursor {
		return 0, fmt.Errorf("invalid_cursor")
	}
	return n, nil
}

// EncodeFrame serializes validated SDK events. D04 adds authorized, persisted
// stream delivery and flush/deadline handling around these bytes.
func EncodeFrame(w io.Writer, sequence string, event events.Event) error {
	n, err := ParseCursor(sequence)
	if err != nil || n == 0 {
		return fmt.Errorf("invalid_cursor")
	}
	if event == nil {
		return fmt.Errorf("missing event")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", sequence, compact.Bytes())
	return err
}
