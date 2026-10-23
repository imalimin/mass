package einoadapter

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// FakeChatModel is programmable test infrastructure. Bootstrap never constructs it.
type FakeChatModel struct {
	Respond func(context.Context, []*schema.Message) ([]*schema.Message, error)
	Tools   []*schema.ToolInfo
}

func (f *FakeChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	copy := *f
	copy.Tools = append([]*schema.ToolInfo(nil), tools...)
	return &copy, nil
}
func (f *FakeChatModel) Generate(ctx context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	chunks, err := f.Respond(ctx, in)
	if err != nil {
		return nil, err
	}
	return schema.ConcatMessages(chunks)
}
func (f *FakeChatModel) Stream(ctx context.Context, in []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	chunks, err := f.Respond(ctx, in)
	if err != nil {
		return nil, err
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		for _, m := range chunks {
			select {
			case <-ctx.Done():
				writer.Send(nil, ctx.Err())
				return
			default:
			}
			if writer.Send(m, nil) {
				return
			}
		}
	}()
	return reader, nil
}
