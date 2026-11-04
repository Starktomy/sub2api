// Copyright (c) 2026 hank9999. Licensed under MIT.
// Copyright (c) 2026 Harllan He. Licensed under MIT.

// Package kiro implements a Kiro protocol core and bounded single-attempt HTTP
// transport. It is not a registered, verified or production-ready upstream adapter.
//
// Primary reference: https://github.com/hank9999/kiro.rs at
// 8fb3dd691d1cedf437bd6a4e9ef9f60b3f217fdd, src/kiro/parser/{frame,header,crc}.rs
// and src/kiro/model/{events,requests}. MIT, Copyright (c) 2026 hank9999.
// Secondary reference: kiro2cc-proxy at
// e5387453c10a87e350fbe2d7c6505a1b128f5445, src/kiro/model/events/
// {assistant,tool_use,context_usage,metering}.rs and model/requests/kiro.rs.
// Its file headers are preserved here: Copyright (c) 2026 Harllan He. MIT.
// Both notices and the permission text are in LICENSE.
//
// DTOs and framing rules are adapted into Go. The existing service/bedrock_stream.go
// was compared: its private decoder filters chunk events and is service-coupled,
// so it cannot supply raw Kiro frames without changing the existing service.
// This package deliberately does not migrate that decoder. CRC is IEEE/ISO-HDLC,
// not CRC32C. Unlike reference recovery paths, corrupt frames fail closed.
// Unlike reference Metering(())/Unknown{}, raw bytes are retained with bounds.
//
// Neither reference establishes a provider success terminal event. Transport EOF,
// tool stop, metering, context usage and unknown events never imply success.
// No context-to-token estimates, usage scaling, cache simulation, SSE, token
// refresh or identity generation are included. messages.go implements a strict
// subset of the primary request DTOs (text and client-tool-only history). It
// rejects system prompts: the reference converter.rs build_history injects a
// synthetic user/assistant pair and policy text, not a native system field.
// Mixed text/tool ordering, history repair, tool filtering and model aliases
// are deliberately not inherited.
//
// Supplemental read-only comparison: AIClient2API a000ac802d87fa06afe6134baaeea3ed350bd08e,
// src/providers/claude/claude-kiro.js parseEventStreamChunk toolUseEvent branch
// (~2510): tool start, input continuation (optional ID), and stop-only payloads.
// GPLv3 source was inspected for wire-shape evidence only; no implementation
// code was copied. The Go resolver independently rejects ambiguous anonymous
// continuations instead of guessing a current tool. No placeholder tools,
// bracket-call extraction, name rewriting or JSON repair are imported.
//
// Terminal investigation: primary src/anthropic/stream.rs get_stop_reason
// defaults locally to end_turn/tool_use; handlers.rs generates final events
// both for Some(Err(_)) and None (EOF). These are synthesized compatibility
// events, not upstream terminal evidence. The inspected primary/fork event DTOs
// establish no explicit application terminal. Result.TransportEOF distinguishes
// clean transport exhaustion from truncation, cancellation and error while
// CompletionVerified remains false. One text event then clean EOF may be a
// legitimate response, but neither success nor provider truncation is proven.
//
// transport.go is a Go implementation referencing the same primary revision's
// src/kiro/endpoint/ide.rs for endpoint path and header names/constant values;
// MIT, Copyright (c) 2026 hank9999 (notice retained in LICENSE). It does not copy
// the reference's generated KiroIDE/machine identity, body injection, retry,
// alternate endpoint selection or API-key mode. Identity headers must be supplied
// explicitly by the caller; service acceptance and requirements remain unverified.
// Offline tests do not establish authorization or successful real generation.
package kiro
