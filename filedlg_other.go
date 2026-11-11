//go:build !windows

package main

import "errors"

// PickFile has no native dialog outside Windows; the window is a Windows
// program, and the terminal node takes a path on its command line.
func PickFile() (string, error) {
	return "", errors.New("这个平台没有文件选择框，请用 lumend -send 路径")
}

// PickFiles is PickFile without the multi-selection.
func PickFiles() ([]string, error) {
	one, err := PickFile()
	if err != nil {
		return nil, err
	}
	return []string{one}, nil
}

// PickFolder is the same story as PickFile.
func PickFolder(string) (string, error) {
	return "", errors.New("这个平台没有文件夹选择框")
}

// allowForeground is a Windows habit; elsewhere the window manager decides.
func allowForeground() {}

// bringFolderToFront is a Windows problem.
func bringFolderToFront(string) {}
