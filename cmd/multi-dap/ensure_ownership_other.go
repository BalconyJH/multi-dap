//go:build !windows

package main

func acquireEnsureChildOwnership() error { return nil }
