package agui

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Live struct {
	mu   sync.Mutex
	runs map[string]*liveRun
}

type liveRun struct {
	checkpoint Checkpoint
	frames     []Frame
	attempt    string
	text       strings.Builder
	lastUsed   time.Time
	finished   bool
}

func (l *Live) stream(id string) *liveRun {
	if l.runs == nil {
		l.runs = make(map[string]*liveRun)
	}
	s := l.runs[id]
	if s == nil {
		s = &liveRun{checkpoint: Checkpoint{StreamID: uuid.NewString()}}
		l.runs[id] = s
	}
	s.lastUsed = time.Now()
	return s
}

func (l *Live) Delta(space, id, attempt, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.stream(space + "/" + id)
	if s.attempt != attempt {
		s.attempt = attempt
		s.text.Reset()
	}
	s.text.WriteString(text)
}

func (l *Live) Text(id, attempt string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.runs[id]
	if s != nil && s.attempt == attempt {
		return s.text.String()
	}
	return ""
}

func (l *Live) Checkpoint(id string) Checkpoint {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stream(id).checkpoint
}

func (l *Live) Commit(id string, old, next Checkpoint, frames []Frame) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.stream(id)
	if s.checkpoint.StreamID != old.StreamID || s.checkpoint.Source != old.Source || s.checkpoint.Cursor != old.Cursor || s.checkpoint.Closed {
		return false
	}
	s.checkpoint = next
	s.frames = append(s.frames, frames...)
	if next.Closed {
		s.finished = true
		s.text.Reset()
	}
	return true
}

func (l *Live) Frames(id string, after int64, limit int) ([]Frame, bool, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.stream(id)
	if after > s.checkpoint.Cursor {
		return nil, false, false
	}
	end := min(after+int64(limit), s.checkpoint.Cursor)
	return append([]Frame{}, s.frames[after:end]...), s.checkpoint.Closed && end == s.checkpoint.Cursor, true
}

func (l *Live) Finished(space, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.runs[space+"/"+id]; s != nil {
		s.finished = true
	}
}

func (l *Live) Expire(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, s := range l.runs {
		if (s.finished || s.text.Len() == 0) && now.Sub(s.lastUsed) > time.Minute {
			delete(l.runs, id)
		}
	}
}
