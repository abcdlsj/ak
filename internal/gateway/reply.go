package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/abcdlsj/ak/internal/translate"
)

// translated stands between an upstream speaking one protocol and a client
// speaking another. A stream is converted event by event as it arrives; a
// whole reply is held until finish and converted at once; an error reply
// becomes the client protocol's error with the upstream's message.
type translated struct {
	w        http.ResponseWriter
	from, to translate.Protocol
	header   http.Header

	status  int
	stream  *translate.Stream
	pending []byte // a stream's incomplete event, or the whole plain body
	broken  bool   // the stream could not be converted; the rest is dropped
}

func newTranslated(w http.ResponseWriter, from, to translate.Protocol) *translated {
	return &translated{w: w, from: from, to: to, header: http.Header{}}
}

// Header is the upstream's headers, kept apart: only the ones that still
// hold after conversion reach the client.
func (t *translated) Header() http.Header { return t.header }

func (t *translated) WriteHeader(code int) {
	if t.status != 0 {
		return
	}
	t.status = code
	if code < 300 && isEventStream(t.header) {
		t.stream = translate.NewStream(t.from, t.to)
		h := t.w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		t.w.WriteHeader(code)
	}
}

func (t *translated) Write(b []byte) (int, error) {
	if t.status == 0 {
		t.WriteHeader(http.StatusOK)
	}
	t.pending = append(t.pending, b...)
	if t.stream == nil {
		if len(t.pending) > maxBodyBytes {
			t.pending = t.pending[:maxBodyBytes]
		}
		return len(b), nil
	}
	for {
		i, sep := eventBoundary(t.pending)
		if i < 0 {
			break
		}
		ev := t.pending[:i]
		t.pending = t.pending[i+sep:]
		if err := t.event(ev); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// event converts one upstream event and writes what it becomes.
func (t *translated) event(ev []byte) error {
	if t.broken {
		return nil
	}
	data := eventData(ev)
	if data == nil {
		return nil
	}
	out, err := t.stream.Event(data)
	if err != nil {
		// What cannot be converted ends the stream with an error the agent
		// shows, rather than a reply it cannot assemble.
		t.broken = true
		out = errorEvent(t.to, "ak: translating the reply: "+err.Error())
	}
	if len(out) == 0 {
		return nil
	}
	_, werr := t.w.Write(out)
	return werr
}

func (t *translated) Flush() {
	if f, ok := t.w.(http.Flusher); ok && t.stream != nil {
		f.Flush()
	}
}

// finish writes what was held back: the end of a stream, or the whole
// converted reply.
func (t *translated) finish() {
	if t.stream != nil {
		if len(bytes.TrimSpace(t.pending)) > 0 {
			_ = t.event(t.pending)
		}
		if !t.broken {
			t.w.Write(t.stream.Close())
		}
		t.Flush()
		return
	}
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	body := t.pending
	if status < 300 {
		out, err := translate.Response(t.from, t.to, body)
		if err != nil {
			status, out = http.StatusBadGateway, translate.Error(t.to, http.StatusBadGateway, "ak: translating the reply: "+err.Error())
		}
		body = out
	} else {
		body = translate.Error(t.to, status, upstreamMessage(body))
	}
	if ra := t.header.Get("Retry-After"); ra != "" {
		t.w.Header().Set("Retry-After", ra)
	}
	t.w.Header().Set("Content-Type", "application/json")
	t.w.WriteHeader(status)
	t.w.Write(body)
}

// eventData is the data of one server-sent event, or nil.
func eventData(ev []byte) []byte {
	var data []byte
	for _, ln := range bytes.Split(ev, []byte("\n")) {
		ln = bytes.TrimRight(ln, "\r")
		if bytes.HasPrefix(ln, []byte("data:")) {
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, bytes.TrimSpace(ln[5:])...)
		}
	}
	return data
}

// errorEvent is a stream's error event in the client's protocol.
func errorEvent(to translate.Protocol, msg string) []byte {
	body := translate.Error(to, http.StatusBadGateway, msg)
	if to == translate.Anthropic {
		return append(append([]byte("event: error\ndata: "), body...), "\n\n"...)
	}
	return append(append([]byte("data: "), body...), "\n\n"...)
}

// upstreamMessage is the message of an upstream's error body in any of the
// protocols, else the body itself, shortened.
func upstreamMessage(body []byte) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &v) == nil {
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(v.Error, &e) == nil && e.Message != "" {
			return e.Message
		}
		var s string
		if json.Unmarshal(v.Error, &s) == nil && s != "" {
			return s
		}
		if v.Message != "" {
			return v.Message
		}
	}
	return snippet(body)
}
