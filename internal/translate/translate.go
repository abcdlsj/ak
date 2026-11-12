// Package translate converts requests, replies and event streams between the
// three wire protocols agents speak: Anthropic Messages, OpenAI Chat
// Completions and OpenAI Responses. The service uses it only when a route's
// upstream speaks another protocol than the client; a request with an
// upstream of its own protocol never comes here.
//
// The conversions are bifrost's (github.com/maximhq/bifrost/core), through
// one hub: every protocol is read into bifrost's Responses types and written
// out of them, so each pair is two steps and the pairs agree with each other.
// Nothing else in ak imports bifrost.
package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// Protocol is a wire protocol.
type Protocol string

// The protocols.
const (
	Anthropic Protocol = "anthropic" // POST /v1/messages
	Chat      Protocol = "chat"      // POST /v1/chat/completions
	Responses Protocol = "responses" // POST /v1/responses
)

// Protocols lists them in display order.
var Protocols = []Protocol{Anthropic, Responses, Chat}

// Supported reports whether a request in from can be served by an upstream
// speaking to. A protocol always serves itself, untranslated.
func Supported(from, to Protocol) bool { return known(from) && known(to) }

func known(p Protocol) bool { return p == Anthropic || p == Chat || p == Responses }

// newCtx is the context bifrost's converters keep their per-call state in.
func newCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

// Request converts a client's request body in from into one for an upstream
// speaking to, asking it for model. Whether a stream was asked for is kept,
// and a chat upstream is asked to report usage at the stream's end.
func Request(from, to Protocol, body []byte, model string) ([]byte, error) {
	ctx := newCtx()
	hub, err := readRequest(ctx, from, body)
	if err != nil {
		return nil, fmt.Errorf("read %s request: %w", from, err)
	}
	stream := wantsStream(body)
	var out any
	switch to {
	case Anthropic:
		r, err := anthropic.ToAnthropicResponsesRequest(ctx, hub)
		if err != nil {
			return nil, err
		}
		r.Model, r.Stream = model, flag(stream)
		out = r
	case Responses:
		r := openai.ToOpenAIResponsesRequest(ctx, hub)
		if r == nil {
			return nil, errors.New("empty request")
		}
		r.Model, r.Stream = model, flag(stream)
		out = r
	case Chat:
		r := openai.ToOpenAIChatRequest(ctx, hub.ToChatRequest())
		if r == nil {
			return nil, errors.New("empty request")
		}
		r.Model, r.Stream = model, flag(stream)
		if stream {
			r.StreamOptions = &schemas.ChatStreamOptions{IncludeUsage: flag(true)}
		}
		out = r
	default:
		return nil, fmt.Errorf("unknown protocol %q", to)
	}
	return json.Marshal(out)
}

// readRequest reads a client request into the hub.
func readRequest(ctx *schemas.BifrostContext, p Protocol, body []byte) (*schemas.BifrostResponsesRequest, error) {
	var hub *schemas.BifrostResponsesRequest
	switch p {
	case Anthropic:
		var r anthropic.AnthropicMessageRequest
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		hub = r.ToBifrostResponsesRequest(ctx)
	case Responses:
		var r openai.OpenAIResponsesRequest
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		hub = r.ToBifrostResponsesRequest(ctx)
	case Chat:
		var r openai.OpenAIChatRequest
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		if c := r.ToBifrostChatRequest(ctx); c != nil {
			hub = c.ToResponsesRequest()
		}
	default:
		return nil, fmt.Errorf("unknown protocol %q", p)
	}
	if hub == nil {
		return nil, errors.New("nothing to convert")
	}
	return hub, nil
}

// Response converts a whole, non-streamed reply of an upstream speaking from
// into the client's protocol to.
func Response(from, to Protocol, body []byte) ([]byte, error) {
	ctx := newCtx()
	var hub *schemas.BifrostResponsesResponse
	switch from {
	case Anthropic:
		var r anthropic.AnthropicMessageResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		hub = r.ToBifrostResponsesResponse(ctx)
	case Responses:
		hub = &schemas.BifrostResponsesResponse{}
		if err := json.Unmarshal(body, hub); err != nil {
			return nil, err
		}
	case Chat:
		var r schemas.BifrostChatResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		hub = r.ToBifrostResponsesResponse()
	default:
		return nil, fmt.Errorf("unknown protocol %q", from)
	}
	if hub == nil {
		return nil, errors.New("empty reply")
	}
	switch to {
	case Anthropic:
		return json.Marshal(anthropic.ToAnthropicResponsesResponse(ctx, hub))
	case Responses:
		return wire(hub)
	case Chat:
		return wire(hub.ToBifrostChatResponse())
	}
	return nil, fmt.Errorf("unknown protocol %q", to)
}

// Stream converts one upstream event stream, event by event. It is not safe
// for concurrent use; each reply gets its own.
type Stream struct {
	from, to Protocol
	ctx      *schemas.BifrostContext
	seq      int
	anth     *anthropic.AnthropicResponsesStreamState
	chat     *schemas.ChatToResponsesStreamState
	// usage gathers what an Anthropic stream reports on message_start and
	// message_delta: bifrost's converter leaves it to its own accumulator,
	// so the completed event carries it only when it is put there.
	usage anthropic.AnthropicUsage
	// held is a chat stream's completed event, kept back until the usage
	// chunk a chat upstream sends after its finish.
	held *schemas.BifrostResponsesStreamResponse
}

// NewStream starts converting a stream from an upstream speaking from to a
// client speaking to.
func NewStream(from, to Protocol) *Stream {
	s := &Stream{from: from, to: to, ctx: newCtx()}
	switch from {
	case Anthropic:
		s.anth = anthropic.AcquireAnthropicResponsesStreamState()
	case Chat:
		s.chat = schemas.AcquireChatToResponsesStreamState()
	}
	return s
}

// Event converts the data of one upstream event into zero or more framed
// events for the client. An event that does not parse is an error; the
// caller decides whether the stream can go on.
func (s *Stream) Event(data []byte) ([]byte, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var hub []*schemas.BifrostResponsesStreamResponse
	if bytes.Equal(data, []byte("[DONE]")) {
		hub = s.release()
	} else {
		var err error
		if hub, err = s.read(data); err != nil {
			return nil, err
		}
	}
	return s.frames(hub)
}

// frames writes hub events for the client.
func (s *Stream) frames(hub []*schemas.BifrostResponsesStreamResponse) ([]byte, error) {
	var out bytes.Buffer
	for _, ev := range hub {
		if err := s.write(&out, ev); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

// read turns one upstream event into hub events.
func (s *Stream) read(data []byte) ([]*schemas.BifrostResponsesStreamResponse, error) {
	switch s.from {
	case Anthropic:
		var ev anthropic.AnthropicStreamEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil, err
		}
		s.gather(ev)
		out, berr, _ := ev.ToBifrostResponsesStream(s.ctx, s.seq, s.anth)
		if berr != nil {
			return nil, bifrostErr(berr)
		}
		s.seq += len(out)
		for _, o := range out {
			if o.Type == schemas.ResponsesStreamResponseTypeCompleted && o.Response != nil && o.Response.Usage == nil {
				o.Response.Usage = anthropic.ConvertAnthropicUsageToBifrostUsage(&s.usage)
			}
		}
		return out, nil
	case Responses:
		var ev schemas.BifrostResponsesStreamResponse
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil, err
		}
		return []*schemas.BifrostResponsesStreamResponse{&ev}, nil
	case Chat:
		var ev schemas.BifrostChatResponse
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil, err
		}
		if s.held != nil {
			if ev.Usage != nil {
				s.held.Response.Usage = ev.Usage.ToResponsesResponseUsage()
			}
			return s.release(), nil
		}
		var out []*schemas.BifrostResponsesStreamResponse
		for _, o := range ev.ToBifrostResponsesStreamResponse(s.chat) {
			if o.Type == schemas.ResponsesStreamResponseTypeCompleted && o.Response != nil && o.Response.Usage == nil {
				s.held = o
				continue
			}
			out = append(out, o)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown protocol %q", s.from)
}

// gather keeps the largest count of each kind an Anthropic stream reports:
// input and cache on message_start, output (cumulative) on message_delta.
func (s *Stream) gather(ev anthropic.AnthropicStreamEvent) {
	u := ev.Usage
	if ev.Message != nil && ev.Message.Usage != nil {
		u = ev.Message.Usage
	}
	if u == nil {
		return
	}
	s.usage.InputTokens = max(s.usage.InputTokens, u.InputTokens)
	s.usage.OutputTokens = max(s.usage.OutputTokens, u.OutputTokens)
	s.usage.CacheReadInputTokens = max(s.usage.CacheReadInputTokens, u.CacheReadInputTokens)
	s.usage.CacheCreationInputTokens = max(s.usage.CacheCreationInputTokens, u.CacheCreationInputTokens)
}

// write frames one hub event in the client's protocol.
func (s *Stream) write(out *bytes.Buffer, ev *schemas.BifrostResponsesStreamResponse) error {
	switch s.to {
	case Anthropic:
		for _, a := range anthropic.ToAnthropicResponsesStreamResponse(s.ctx, ev) {
			if a == nil {
				continue
			}
			b, err := json.Marshal(a)
			if err != nil {
				return err
			}
			frame(out, string(a.Type), b)
		}
	case Responses:
		b, err := wire(ev)
		if err != nil {
			return err
		}
		frame(out, string(ev.Type), b)
	case Chat:
		c := ev.ToBifrostChatResponse()
		if c == nil || (!saysSomething(c) && c.Usage == nil) {
			return nil
		}
		b, err := wire(c)
		if err != nil {
			return err
		}
		frame(out, "", b)
	default:
		return fmt.Errorf("unknown protocol %q", s.to)
	}
	return nil
}

// saysSomething reports whether a chat chunk carries a delta or a finish:
// the hub's bookkeeping events become empty chunks, which are left out.
func saysSomething(c *schemas.BifrostChatResponse) bool {
	for _, ch := range c.Choices {
		if ch.FinishReason != nil {
			return true
		}
		if d := ch.ChatStreamResponseChoice; d != nil && d.Delta != nil {
			if b, _ := json.Marshal(d.Delta); string(b) != "{}" {
				return true
			}
		}
	}
	return false
}

// release hands over a held completed event, if any.
func (s *Stream) release() []*schemas.BifrostResponsesStreamResponse {
	if s.held == nil {
		return nil
	}
	h := s.held
	s.held = nil
	return []*schemas.BifrostResponsesStreamResponse{h}
}

// Close ends the stream: an event still held, then what the client's
// protocol sends last.
func (s *Stream) Close() []byte {
	out, _ := s.frames(s.release())
	if s.to == Chat {
		out = append(out, "data: [DONE]\n\n"...)
	}
	return out
}

// frame writes one server-sent event.
func frame(out *bytes.Buffer, event string, data []byte) {
	if event != "" {
		out.WriteString("event: " + event + "\n")
	}
	out.WriteString("data: ")
	out.Write(data)
	out.WriteString("\n\n")
}

// wire marshals a bifrost value as the OpenAI wire has it: without the
// extra_fields bifrost adds for its own bookkeeping.
func wire(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return b, nil
	}
	if _, ok := m["extra_fields"]; !ok {
		return b, nil
	}
	delete(m, "extra_fields")
	return json.Marshal(m)
}

// Error is an error reply body in the client's protocol, so the agent
// shows the message rather than a parse failure.
func Error(to Protocol, status int, message string) []byte {
	var v any
	if to == Anthropic {
		v = map[string]any{"type": "error", "error": map[string]string{"type": anthropicErrorType(status), "message": message}}
	} else {
		v = map[string]any{"error": map[string]any{"message": message, "type": openaiErrorType(status), "code": status}}
	}
	b, _ := json.Marshal(v)
	return b
}

func anthropicErrorType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	}
	return "api_error"
}

func openaiErrorType(status int) string {
	switch {
	case status == 401:
		return "authentication_error"
	case status == 403:
		return "permission_error"
	case status == 404:
		return "not_found_error"
	case status == 429:
		return "rate_limit_error"
	case status < 500:
		return "invalid_request_error"
	}
	return "server_error"
}

// bifrostErr is a bifrost error as a Go one.
func bifrostErr(e *schemas.BifrostError) error {
	if e.Error != nil && e.Error.Message != "" {
		return errors.New(e.Error.Message)
	}
	return errors.New("stream conversion failed")
}

// wantsStream reports whether a request body asks for a streamed reply.
func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &v) == nil && v.Stream
}

func flag(b bool) *bool {
	if !b {
		return nil
	}
	return &b
}
