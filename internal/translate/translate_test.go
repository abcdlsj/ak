package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in testdata are written by hand from the protocols, in the
// shapes Claude Code and Codex send: a tool-call turn with its result, a
// reply that thinks, says something and calls a tool, and the same reply
// streamed.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// requestFixture is each protocol's recorded request.
var requestFixture = map[Protocol]string{
	Anthropic: "claude_request.json",
	Responses: "codex_request.json",
	Chat:      "chat_request.json",
}

// turn is what a request carries, whatever its protocol.
type turn struct {
	system   string
	tools    []string
	callIDs  []string // tool calls made earlier in the conversation
	resultID []string // tool results answering them
	stream   bool
	model    string
}

func readTurn(t *testing.T, p Protocol, body []byte) turn {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%s request does not parse: %v\n%s", p, err, body)
	}
	tr := turn{model: str(v["model"]), stream: v["stream"] == true}
	switch p {
	case Anthropic:
		tr.system = textOf(v["system"])
		for _, x := range list(v["tools"]) {
			tr.tools = append(tr.tools, str(obj(x)["name"]))
		}
		for _, m := range list(v["messages"]) {
			for _, b := range list(obj(m)["content"]) {
				switch b := obj(b); b["type"] {
				case "tool_use":
					tr.callIDs = append(tr.callIDs, str(b["id"]))
				case "tool_result":
					tr.resultID = append(tr.resultID, str(b["tool_use_id"]))
				}
			}
		}
	case Responses:
		tr.system = str(v["instructions"])
		for _, x := range list(v["tools"]) {
			tr.tools = append(tr.tools, str(obj(x)["name"]))
		}
		for _, x := range list(v["input"]) {
			switch x := obj(x); {
			case x["type"] == "function_call":
				tr.callIDs = append(tr.callIDs, str(x["call_id"]))
			case x["type"] == "function_call_output":
				tr.resultID = append(tr.resultID, str(x["call_id"]))
			case x["role"] == "system" || x["role"] == "developer":
				tr.system += textOf(x["content"])
			}
		}
	case Chat:
		for _, x := range list(v["tools"]) {
			tr.tools = append(tr.tools, str(obj(obj(x)["function"])["name"]))
		}
		for _, m := range list(v["messages"]) {
			m := obj(m)
			switch m["role"] {
			case "system", "developer":
				tr.system += textOf(m["content"])
			case "assistant":
				for _, c := range list(m["tool_calls"]) {
					tr.callIDs = append(tr.callIDs, str(obj(c)["id"]))
				}
			case "tool":
				tr.resultID = append(tr.resultID, str(m["tool_call_id"]))
			}
		}
	}
	return tr
}

func TestRequestEveryPair(t *testing.T) {
	for _, from := range Protocols {
		for _, to := range Protocols {
			if from == to {
				continue
			}
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				in := fixture(t, requestFixture[from])
				want := readTurn(t, from, in)
				out, err := Request(from, to, in, "upstream-model")
				if err != nil {
					t.Fatal(err)
				}
				got := readTurn(t, to, out)
				if got.model != "upstream-model" {
					t.Errorf("model = %q, want the upstream's", got.model)
				}
				if !got.stream {
					t.Errorf("the stream was dropped:\n%s", out)
				}
				if !strings.Contains(got.system, firstWords(want.system)) {
					t.Errorf("system = %q, want %q", got.system, want.system)
				}
				if strings.Join(got.tools, ",") != strings.Join(want.tools, ",") {
					t.Errorf("tools = %v, want %v", got.tools, want.tools)
				}
				if strings.Join(got.callIDs, ",") != strings.Join(want.callIDs, ",") ||
					strings.Join(got.resultID, ",") != strings.Join(want.resultID, ",") {
					t.Errorf("tool turn = %v/%v, want %v/%v\n%s", got.callIDs, got.resultID, want.callIDs, want.resultID, out)
				}
			})
		}
	}
}

func TestChatUpstreamReportsStreamUsage(t *testing.T) {
	out, err := Request(Anthropic, Chat, fixture(t, "claude_request.json"), "m")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"include_usage":true`)) {
		t.Errorf("a streamed chat request does not ask for usage:\n%s", out)
	}
}

// reply is what an assistant turn says, whatever its protocol.
type reply struct {
	text, thinking, tool, args string
	input, output              float64
	ended                      bool // a stream reached its protocol's end
}

// The recorded replies all say this.
var wantReply = reply{
	text:     "It prints hi. Let me check the tests.",
	thinking: "The file prints hi; check the rest.",
	tool:     "Read",
	args:     `{"file_path":"/src/main_test.go"}`,
	output:   45,
}

var replyFixture = map[Protocol]string{
	Anthropic: "anthropic_reply.json",
	Responses: "responses_reply.json",
	Chat:      "chat_reply.json",
}

func readReply(t *testing.T, p Protocol, body []byte) reply {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%s reply does not parse: %v\n%s", p, err, body)
	}
	var r reply
	u := obj(v["usage"])
	switch p {
	case Anthropic:
		for _, b := range list(v["content"]) {
			switch b := obj(b); b["type"] {
			case "text":
				r.text += str(b["text"])
			case "thinking":
				r.thinking += str(b["thinking"])
			case "tool_use":
				r.tool = str(b["name"])
				a, _ := json.Marshal(b["input"])
				r.args = string(a)
			}
		}
		r.input, r.output = num(u["input_tokens"]), num(u["output_tokens"])
	case Responses:
		for _, x := range list(v["output"]) {
			switch x := obj(x); x["type"] {
			case "message":
				r.text += textOf(x["content"])
			case "reasoning":
				r.thinking += textOf(x["summary"]) + textOf(x["content"])
			case "function_call":
				r.tool, r.args = str(x["name"]), str(x["arguments"])
			}
		}
		r.input, r.output = num(u["input_tokens"]), num(u["output_tokens"])
	case Chat:
		m := obj(obj(first(list(v["choices"])))["message"])
		r.text = str(m["content"])
		r.thinking = chatReasoning(m)
		if c := obj(first(list(m["tool_calls"]))); c != nil {
			r.tool, r.args = str(obj(c["function"])["name"]), str(obj(c["function"])["arguments"])
		}
		r.input, r.output = num(u["prompt_tokens"]), num(u["completion_tokens"])
	}
	return r
}

func TestResponseEveryPair(t *testing.T) {
	for _, from := range Protocols {
		for _, to := range Protocols {
			if from == to {
				continue
			}
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				out, err := Response(from, to, fixture(t, replyFixture[from]))
				if err != nil {
					t.Fatal(err)
				}
				checkReply(t, readReply(t, to, out), false, out)
			})
		}
	}
}

func checkReply(t *testing.T, got reply, stream bool, raw []byte) {
	t.Helper()
	if got.text != wantReply.text {
		t.Errorf("text = %q, want %q", got.text, wantReply.text)
	}
	if got.thinking != wantReply.thinking {
		t.Errorf("thinking = %q, want %q", got.thinking, wantReply.thinking)
	}
	if got.tool != wantReply.tool || !sameJSON(got.args, wantReply.args) {
		t.Errorf("tool call = %s %s, want %s %s", got.tool, got.args, wantReply.tool, wantReply.args)
	}
	if got.output != wantReply.output || got.input == 0 {
		t.Errorf("usage = %v in, %v out; want some in, %v out", got.input, got.output, wantReply.output)
	}
	if stream && !got.ended {
		t.Errorf("the stream does not end the way its protocol does")
	}
	if t.Failed() {
		t.Logf("converted:\n%s", raw)
	}
}

var streamFixture = map[Protocol]string{
	Anthropic: "anthropic_stream.sse",
	Responses: "responses_stream.sse",
	Chat:      "chat_stream.sse",
}

// events splits a server-sent event stream into each event's data.
func events(b []byte) [][]byte {
	var out [][]byte
	var data []byte
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		ln := sc.Text()
		switch {
		case ln == "":
			if data != nil {
				out = append(out, data)
			}
			data = nil
		case strings.HasPrefix(ln, "data:"):
			data = append(data, strings.TrimSpace(ln[5:])...)
		}
	}
	if data != nil {
		out = append(out, data)
	}
	return out
}

// readStream assembles a streamed reply.
func readStream(t *testing.T, p Protocol, b []byte) reply {
	t.Helper()
	var r reply
	for _, data := range events(b) {
		if string(data) == "[DONE]" {
			r.ended = p == Chat
			continue
		}
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatalf("%s event does not parse: %v\n%s", p, err, data)
		}
		switch p {
		case Anthropic:
			d := obj(v["delta"])
			switch v["type"] {
			case "message_start":
				r.input = num(obj(obj(v["message"])["usage"])["input_tokens"])
			case "content_block_start":
				if b := obj(v["content_block"]); b["type"] == "tool_use" {
					r.tool = str(b["name"])
				}
			case "content_block_delta":
				r.text += str(d["text"])
				r.thinking += str(d["thinking"])
				r.args += str(d["partial_json"])
			case "message_delta":
				r.output = max(r.output, num(obj(v["usage"])["output_tokens"]))
				if in := num(obj(v["usage"])["input_tokens"]); in > 0 {
					r.input = in
				}
			case "message_stop":
				r.ended = true
			}
		case Responses:
			switch typ := str(v["type"]); typ {
			case "response.output_text.delta":
				r.text += str(v["delta"])
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				r.thinking += str(v["delta"])
			case "response.output_item.added":
				if it := obj(v["item"]); it["type"] == "function_call" {
					r.tool = str(it["name"])
				}
			case "response.function_call_arguments.delta":
				r.args += str(v["delta"])
			case "response.completed":
				u := obj(obj(v["response"])["usage"])
				r.input, r.output, r.ended = num(u["input_tokens"]), num(u["output_tokens"]), true
			}
		case Chat:
			if u := obj(v["usage"]); u != nil {
				r.input, r.output = num(u["prompt_tokens"]), num(u["completion_tokens"])
			}
			for _, c := range list(v["choices"]) {
				d := obj(obj(c)["delta"])
				r.text += str(d["content"])
				r.thinking += chatReasoning(d)
				for _, tc := range list(d["tool_calls"]) {
					f := obj(obj(tc)["function"])
					if n := str(f["name"]); n != "" {
						r.tool = n
					}
					r.args += str(f["arguments"])
				}
			}
		}
	}
	return r
}

func TestStreamEveryPair(t *testing.T) {
	for _, from := range Protocols {
		for _, to := range Protocols {
			if from == to {
				continue
			}
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				s := NewStream(from, to)
				var out bytes.Buffer
				for _, data := range events(fixture(t, streamFixture[from])) {
					b, err := s.Event(data)
					if err != nil {
						t.Fatalf("event %s: %v", data, err)
					}
					out.Write(b)
				}
				out.Write(s.Close())
				checkReply(t, readStream(t, to, out.Bytes()), true, out.Bytes())
			})
		}
	}
}

// The fixtures are self-consistent: each reads as the reply the others are
// converted into.
func TestFixturesAgree(t *testing.T) {
	for _, p := range Protocols {
		checkReply(t, readReply(t, p, fixture(t, replyFixture[p])), false, nil)
		checkReply(t, readStream(t, p, fixture(t, streamFixture[p])), true, nil)
	}
}

func TestErrorShapes(t *testing.T) {
	var a struct {
		Type  string `json:"type"`
		Error struct{ Type, Message string }
	}
	if err := json.Unmarshal(Error(Anthropic, 404, "no route x"), &a); err != nil || a.Type != "error" ||
		a.Error.Type != "not_found_error" || a.Error.Message != "no route x" {
		t.Errorf("anthropic error = %+v (%v)", a, err)
	}
	var o struct {
		Error struct{ Message, Type string }
	}
	if err := json.Unmarshal(Error(Chat, 401, "bad token"), &o); err != nil || o.Error.Message != "bad token" ||
		o.Error.Type != "authentication_error" {
		t.Errorf("openai error = %+v (%v)", o, err)
	}
}

// chatReasoning is a chat message's reasoning: reasoning_content, else
// reasoning, else the summaries in reasoning_details, as clients read them.
func chatReasoning(m map[string]any) string {
	if s := str(m["reasoning_content"]); s != "" {
		return s
	}
	if s := str(m["reasoning"]); s != "" {
		return s
	}
	var b strings.Builder
	for _, d := range list(m["reasoning_details"]) {
		b.WriteString(str(obj(d)["summary"]) + str(obj(d)["text"]))
	}
	return b.String()
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func list(v any) []any         { l, _ := v.([]any); return l }
func str(v any) string         { s, _ := v.(string); return s }
func num(v any) float64        { f, _ := v.(float64); return f }

func first(l []any) any {
	if len(l) == 0 {
		return nil
	}
	return l[0]
}

// textOf flattens a string or a list of text blocks.
func textOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var b strings.Builder
	for _, x := range list(v) {
		b.WriteString(str(obj(x)["text"]))
	}
	return b.String()
}

func firstWords(s string) string {
	if len(s) > 20 {
		return s[:20]
	}
	return s
}

func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}
