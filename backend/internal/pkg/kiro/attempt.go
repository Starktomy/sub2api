// Copyright (c) 2026 hank9999. Licensed under MIT.
// Copyright (c) 2026 Harllan He. Licensed under MIT.
// See provenance.go and LICENSE for adaptation sources.

package kiro

import "io"

// Delta contains only the current chunk; Attempt never accumulates response
// text or tool input. ToolStop ends that tool only. There is no success flag.
type Delta struct {
	Text, ToolUseID, ToolName, ToolInput string
	ToolStop                             bool
}
type toolState struct {
	name    string
	stopped bool
}

// Attempt is per-attempt, single-consumer state. Retained tool metadata is bounded.
// Construct a new Attempt for a new attempt; there is deliberately no reset.
type Attempt struct {
	tools    map[string]toolState
	bytes    int
	terminal error
}

func (a *Attempt) fail(err error) (Delta, error) { a.terminal = err; return Delta{}, err }
func (a *Attempt) Apply(e Event) (Delta, error) {
	if a.terminal != nil {
		return Delta{}, a.terminal
	}
	if e.messageType == "error" || e.messageType == "exception" {
		return a.fail(ErrUpstream)
	}
	if e.messageType != "event" {
		return a.fail(ErrEvent)
	}
	if v, ok := e.Assistant(); ok {
		return Delta{Text: v.Content}, nil
	}
	if v, ok := e.Tool(); ok {
		if a.tools == nil {
			a.tools = make(map[string]toolState)
		}
		if v.ToolUseID == "" {
			// Only a single open tool permits an anonymous continuation.
			for id, state := range a.tools {
				if !state.stopped {
					if v.ToolUseID != "" {
						return a.fail(ErrState)
					}
					v.ToolUseID = id
				}
			}
		}
		previous, exists := a.tools[v.ToolUseID]
		if v.Name == "" {
			if !exists {
				return a.fail(ErrState)
			}
			v.Name = previous.name
		}
		if v.ToolUseID == "" {
			return a.fail(ErrState)
		}
		if exists && (previous.stopped || previous.name != v.Name) {
			return a.fail(ErrState)
		}
		if !exists {
			if len(a.tools) >= 1024 || len(v.ToolUseID)+len(v.Name) > 128*1024-a.bytes {
				return a.fail(ErrLimit)
			}
			a.bytes += len(v.ToolUseID) + len(v.Name)
		}
		a.tools[v.ToolUseID] = toolState{name: v.Name, stopped: v.Stop}
		return Delta{ToolUseID: v.ToolUseID, ToolName: v.Name, ToolInput: v.Input, ToolStop: v.Stop}, nil
	}
	// Context, metering and unknown events are nonterminal observations.
	return Delta{}, nil
}

// End records transport termination, never successful generation completion.
// Pass the decoder error (including EOF). Nil also cannot establish success.
func (a *Attempt) End(err error) error {
	if a.terminal != nil {
		return a.terminal
	}
	if err == nil {
		err = ErrIncomplete
	} else if err == io.EOF {
		err = ErrIncomplete
	} else {
		// Only fixed, locally generated errors are exposed; never echo reader errors.
		switch err {
		case ErrFrame, ErrHeader, ErrLimit, ErrEvent, ErrRead, io.ErrUnexpectedEOF:
		default:
			err = ErrIncomplete
		}
	}
	a.terminal = err
	return err
}
