//go:build !linux && !darwin && !windows

package main

func osVersion() string { return "unknown" }
