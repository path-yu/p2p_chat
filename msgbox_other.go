//go:build !windows

package main

// messageBox and canLoad exist so the window program still builds where it
// is not the target; the mac and linux builds report to stderr instead.

import "fmt"

func messageBox(title, text string, isError bool) {
	fmt.Printf("%s\n%s\n", title, text)
}

func canLoad(string) bool { return true }
