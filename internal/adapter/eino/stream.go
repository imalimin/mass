package einoadapter

import (
	"cmp"
	"slices"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// streamedMessage keeps the merged fields as chunks arrive. The growing text,
// tool arguments and log probabilities are appended without copying their prefix.
type streamedMessage struct {
	metadata       schema.Message
	content        strings.Builder
	reasoning      strings.Builder
	stringExtra    map[string]*strings.Builder
	logProbs       *schema.LogProbs
	unindexedCalls []schema.ToolCall
	indexedCalls   map[int]*streamedToolCall
	invalid        bool
}

func (m *streamedMessage) add(chunk *schema.Message) {
	m.content.WriteString(chunk.Content)
	m.reasoning.WriteString(chunk.ReasoningContent)
	if m.invalid {
		return
	}

	// Keep Eino's merge rules for message metadata and provider-specific fields.
	metadata := *chunk
	metadata.Content, metadata.ReasoningContent, metadata.ToolCalls, metadata.Extra = "", "", nil, nil
	for key, value := range chunk.Extra {
		if part, ok := value.(string); ok {
			if _, conflict := m.metadata.Extra[key]; conflict {
				m.invalid = true
				return
			}
			if m.stringExtra == nil {
				m.stringExtra = make(map[string]*strings.Builder)
			}
			if m.stringExtra[key] == nil {
				m.stringExtra[key] = &strings.Builder{}
			}
			m.stringExtra[key].WriteString(part)
			continue
		}
		if m.stringExtra[key] != nil {
			m.invalid = true
			return
		}
		if metadata.Extra == nil {
			metadata.Extra = make(map[string]any)
		}
		metadata.Extra[key] = value
	}
	if meta := chunk.ResponseMeta; meta != nil && meta.LogProbs != nil {
		if m.logProbs == nil {
			m.logProbs = &schema.LogProbs{}
		}
		m.logProbs.Content = append(m.logProbs.Content, meta.LogProbs.Content...)
		withoutLogProbs := *meta
		withoutLogProbs.LogProbs = nil
		metadata.ResponseMeta = &withoutLogProbs
	}
	merged, err := schema.ConcatMessages([]*schema.Message{&m.metadata, &metadata})
	if err != nil {
		m.invalid = true
		return
	}
	m.metadata = *merged
	for _, call := range chunk.ToolCalls {
		if call.Index == nil {
			m.unindexedCalls = append(m.unindexedCalls, call)
			continue
		}
		index := *call.Index
		if m.indexedCalls == nil {
			m.indexedCalls = make(map[int]*streamedToolCall)
		}
		current := m.indexedCalls[index]
		if current == nil {
			current = &streamedToolCall{call: call}
			current.call.Index = &index
			current.call.Function.Arguments = ""
			m.indexedCalls[index] = current
		}
		if !mergeToolField(&current.call.ID, call.ID) ||
			!mergeToolField(&current.call.Type, call.Type) ||
			!mergeToolField(&current.call.Function.Name, call.Function.Name) {
			m.invalid = true
			return
		}
		current.arguments.WriteString(call.Function.Arguments)
	}
}

func (m *streamedMessage) result() schema.Message {
	result := m.metadata
	result.Content = m.content.String()
	result.ReasoningContent = m.reasoning.String()
	if m.logProbs != nil {
		result.ResponseMeta.LogProbs = m.logProbs
	}
	if len(m.stringExtra) > 0 && result.Extra == nil {
		result.Extra = make(map[string]any, len(m.stringExtra))
	}
	for key, value := range m.stringExtra {
		result.Extra[key] = value.String()
	}
	result.ToolCalls = slices.Grow(m.unindexedCalls, len(m.indexedCalls))
	for _, tool := range m.indexedCalls {
		call := tool.call
		call.Function.Arguments = tool.arguments.String()
		result.ToolCalls = append(result.ToolCalls, call)
	}
	slices.SortFunc(result.ToolCalls[len(m.unindexedCalls):], func(a, b schema.ToolCall) int {
		return cmp.Compare(*a.Index, *b.Index)
	})
	return result
}

type streamedToolCall struct {
	call      schema.ToolCall
	arguments strings.Builder
}

func mergeToolField(current *string, value string) bool {
	if *current == "" {
		*current = value
	}
	return value == "" || *current == value
}
