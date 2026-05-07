package backend

import (
	"rl-toolkit/backend/internal/source"
)

// RLSource and FixtureSource are backend-package aliases over the
// internal/source implementations so existing call sites (main.go,
// server.go, the in-package tests) keep compiling. The constructors
// preserve the old names too.
type RLSource = source.RL
type RLStatus = source.Status

const (
	StatusDisconnected = source.StatusDisconnected
	StatusConnecting   = source.StatusConnecting
	StatusConnected    = source.StatusConnected
)

func NewRLSource(addr string) *RLSource { return source.NewRL(addr) }
