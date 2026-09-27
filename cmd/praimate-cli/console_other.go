//go:build !windows

package main

func enableConsole() (func(), error) { return func() {}, nil }
