//go:build !windows

package tray

type Event int

const (
	EventShow Event = iota + 1
	EventQuit
)

type Tray struct{}

func Start(chan<- Event) (*Tray, error)     { return &Tray{}, nil }
func (t *Tray) Stop()                       {}
func (t *Tray) Notify(string, string) error { return nil }
