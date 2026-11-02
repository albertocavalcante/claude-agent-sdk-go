// Package protocol holds the wire types of the Claude Code CLI's
// bidirectional stream-json protocol (CLI 2.1.x). It performs no I/O.
//
// Stdout lines decode into a Frame: either a Message (what a consumer reads)
// or a control frame (ControlRequest, ControlResponse, ControlCancelRequest)
// that the client's router handles. Every Message and Block keeps the
// original JSON in Raw. Unknown message types, system subtypes, content
// blocks and fields are preserved, never errors, so a newer CLI cannot break
// an older SDK.
//
// Outbound frames are built with the Encode functions: user messages (stamped
// with a uuid and origin {kind: human}), control requests and control
// responses.
package protocol
