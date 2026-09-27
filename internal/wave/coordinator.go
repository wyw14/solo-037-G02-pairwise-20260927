package wave

import (
    "context"
    "sync"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

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
    ticker := time.NewTicker(5 * time.Millisecond)
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

// StartBounded drives the paddle for exactly profile.Duration, sampling every
// step (the first command is emitted immediately at elapsed 0). Each issued
// command is appended to the in-memory history and forwarded through emit.
// The run stops at the deadline, when ctx is cancelled, or when Stop is called
// (the emergency brake shares that path). Unlike Start's diagnostic loop, the
// done channel is always closed when the goroutine exits.
func (c *Controller) StartBounded(ctx context.Context, run model.RunID, profile Profile, epoch uint64, startedAt time.Time, step time.Duration, emit func(model.DeviceCommand)) {
    c.mu.Lock()
    c.run = run
    c.active = true
    c.done = make(chan struct{})
    c.mu.Unlock()
    go c.boundedLoop(ctx, run, profile, epoch, startedAt.UTC(), step, emit)
}

func (c *Controller) boundedLoop(ctx context.Context, run model.RunID, profile Profile, epoch uint64, startedAt time.Time, step time.Duration, emit func(model.DeviceCommand)) {
    defer func() {
        c.mu.Lock()
        c.active = false
        close(c.done)
        c.mu.Unlock()
    }()
    ticker := time.NewTicker(step)
    defer ticker.Stop()
    elapsed := time.Duration(0)
    for {
        if elapsed > profile.Duration {
            return
        }
        c.mu.Lock()
        active := c.active
        c.mu.Unlock()
        if !active || ctx.Err() != nil {
            return
        }
        command := model.DeviceCommand{ID: string(model.NewEventID()), Run: run, Epoch: epoch, Setpoint: profile.Sample(elapsed), IssuedAt: time.Now().UTC()}
        c.mu.Lock()
        c.commands = append(c.commands, command)
        c.mu.Unlock()
        emit(command)
        elapsed += step
        if elapsed > profile.Duration {
            return
        }
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
        }
    }
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
