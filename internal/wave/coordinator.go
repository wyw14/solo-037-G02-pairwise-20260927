package wave

import (
    "context"
    "sync"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

// TickInterval is how often the controller issues a setpoint while running.
const TickInterval = 5 * time.Millisecond

type Controller struct {
    mu sync.Mutex
    run model.RunID
    commands []model.DeviceCommand
    active bool
    done chan struct{}
}

func NewController() *Controller { return &Controller{done: make(chan struct{})} }

func (c *Controller) Start(ctx context.Context, run model.RunID, profile Profile, epoch uint64, at time.Time) {
    c.mu.Lock()
    c.run = run
    c.active = true
    c.done = make(chan struct{})
    c.mu.Unlock()
    go c.loop(ctx, profile, epoch, at)
}

func (c *Controller) loop(ctx context.Context, profile Profile, epoch uint64, at time.Time) {
    ticker := time.NewTicker(TickInterval)
    defer ticker.Stop()
    index := 0
    samples := profile.Samples(profile.Duration / 3)
    for {
        select {
        case <-ctx.Done():
            c.mu.Lock()
            c.active = false
            close(c.done)
            c.mu.Unlock()
            return
        case now := <-ticker.C:
            c.mu.Lock()
            if !c.active {
                c.mu.Unlock()
                return
            }
            value := samples[index%len(samples)]
            c.commands = append(c.commands, model.DeviceCommand{ID: string(model.NewEventID()), Run: c.run, Epoch: epoch, Setpoint: value, IssuedAt: now.UTC()})
            index++
            c.mu.Unlock()
            _ = at
        }
    }
}

func (c *Controller) Stop() {
    c.mu.Lock()
    c.active = false
    c.mu.Unlock()
}

func (c *Controller) Active() bool {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.active
}

func (c *Controller) Commands() []model.DeviceCommand {
    c.mu.Lock()
    defer c.mu.Unlock()
    result := make([]model.DeviceCommand, len(c.commands))
    copy(result, c.commands)
    return result
}

func (c *Controller) WaitStopped(timeout time.Duration) bool {
    c.mu.Lock()
    done := c.done
    c.mu.Unlock()
    select {
    case <-done:
        return true
    case <-time.After(timeout):
        return false
    }
}
