package core

import "testing"

// TestWaitRule: an action's state follows its "waiting for", and the name
// goes when the action is next again.
func TestWaitRule(t *testing.T) {
	for _, c := range []struct {
		name      string
		run       func(w *world) error
		state, on string
	}{
		{"naming someone on a next action", func(w *world) error {
			return w.patch("robin", "a", "waiting_on", "Kim")
		}, StWaiting, "Kim"},
		{"another name while waiting", func(w *world) error {
			w.must(w.patch("robin", "a", "waiting_on", "Kim"))
			return w.patch("robin", "a", "waiting_on", "Lee")
		}, StWaiting, "Lee"},
		{"the name cleared", func(w *world) error {
			w.must(w.patch("robin", "a", "waiting_on", "Kim"))
			return w.patch("robin", "a", "waiting_on", "")
		}, StNext, ""},
		{"back to next", func(w *world) error {
			w.must(w.patch("robin", "a", "waiting_on", "Kim"))
			return w.move("robin", "a", StNext)
		}, StNext, ""},
		{"waiting without a name stays as it is", func(w *world) error {
			return w.move("robin", "a", StWaiting)
		}, StWaiting, ""},
		{"done keeps who it waited for", func(w *world) error {
			w.must(w.patch("robin", "a", "waiting_on", "Kim"))
			return w.move("robin", "a", StDone)
		}, StDone, "Kim"},
		{"someday is not touched", func(w *world) error {
			w.must(w.move("robin", "a", StSomeday))
			return w.patch("robin", "a", "waiting_on", "Kim")
		}, StSomeday, "Kim"},
		{"another field changes nothing", func(w *world) error {
			return w.patch("robin", "a", "title", "renamed")
		}, StNext, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := personal(t)
			w.action("a", "robin")
			if err := c.run(w); err != nil {
				t.Fatal(err)
			}
			if it := w.st.Items["a"]; it.State != c.state || it.WaitingOn != c.on {
				t.Errorf("state %s waiting for %q, want %s %q", it.State, it.WaitingOn, c.state, c.on)
			}
		})
	}
}
