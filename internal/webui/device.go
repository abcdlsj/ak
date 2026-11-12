package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/login"
	"github.com/abcdlsj/ak/internal/vendor"
)

// deviceTimeout bounds a device sign-in the browser left.
const deviceTimeout = 15 * time.Minute

// devices are the device sign-ins under way. Each runs on the server, which
// polls the vendor; the page shows the code and asks how it went.
type devices struct {
	mu sync.Mutex
	m  map[string]*deviceRun
}

type deviceRun struct {
	session string // only the session that started it may see it
	account string
	prompt  login.Prompt
	ready   chan struct{} // closed once the prompt is known or it failed
	done    bool
	err     string
}

func newDevices() *devices { return &devices{m: map[string]*deviceRun{}} }

func (d *devices) get(id, session string) (*deviceRun, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.m[id]
	if !ok || r.session != session {
		return nil, false
	}
	return r, true
}

// startDevice begins a device sign-in and shows its code.
func (c *call) startDevice() {
	vid, name := field(c.r, "vendor"), field(c.r, "name")
	if name == "" {
		name = vid
	}
	v, ok := vendor.Lookup(vid)
	dev := deviceLogin(v)
	if !ok || dev == nil {
		c.back("accounts", done(errNoDevice, ""))
		return
	}
	if err := config.ValidateAccountName(name); err != nil {
		c.back("accounts", done(err, ""))
		return
	}
	id := randomID()
	run := &deviceRun{session: c.id, account: name, ready: make(chan struct{})}
	c.u.devices.mu.Lock()
	c.u.devices.m[id] = run
	c.u.devices.mu.Unlock()
	go c.u.runDevice(id, run, vid, *dev)
	select {
	case <-run.ready:
	case <-time.After(20 * time.Second):
	}
	http.Redirect(c.w, c.r, "/ui/accounts/device?id="+id, http.StatusSeeOther)
}

// runDevice signs in and saves the account, recording how it went.
func (u *UI) runDevice(id string, run *deviceRun, vid string, dev vendor.Device) {
	ctx, cancel := context.WithTimeout(context.Background(), deviceTimeout)
	defer cancel()
	var once sync.Once
	ready := func() { once.Do(func() { close(run.ready) }) }
	creds, err := login.Device(ctx, dev, func(p login.Prompt) {
		u.devices.mu.Lock()
		run.prompt = p
		u.devices.mu.Unlock()
		ready()
	})
	if err == nil {
		u.mu.Lock()
		var cfg *config.Config
		if cfg, err = core.Load(); err == nil {
			if err = core.SignIn(cfg, run.account, vid, creds); err == nil {
				u.b.Reload(cfg)
				u.b.Forget(run.account)
			}
		}
		u.mu.Unlock()
	}
	u.devices.mu.Lock()
	run.done = true
	if err != nil {
		run.err = err.Error()
	}
	u.devices.mu.Unlock()
	ready()
	// The result is kept a while for the page to read, then forgotten.
	time.AfterFunc(time.Minute, func() {
		u.devices.mu.Lock()
		delete(u.devices.m, id)
		u.devices.mu.Unlock()
	})
}

func (c *call) device() {
	id := c.r.URL.Query().Get("id")
	run, ok := c.u.devices.get(id, c.id)
	if !ok {
		c.back("accounts", "That sign-in is over.")
		return
	}
	c.u.devices.mu.Lock()
	data := map[string]any{"ID": id, "Account": run.account, "Code": run.prompt.Code, "URL": run.prompt.URL, "Err": run.err, "Done": run.done}
	c.u.devices.mu.Unlock()
	c.u.render(c.w, "device", data, c)
}

func (c *call) deviceStatus() {
	run, ok := c.u.devices.get(c.r.URL.Query().Get("id"), c.id)
	st := map[string]any{"done": true, "error": "that sign-in is over"}
	if ok {
		c.u.devices.mu.Lock()
		st = map[string]any{"done": run.done, "error": run.err}
		c.u.devices.mu.Unlock()
	}
	c.w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(c.w).Encode(st)
}
