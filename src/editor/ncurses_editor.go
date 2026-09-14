package main

import (
	"fmt"
	//"path/filepath"
	"time"

	libtrecs "trecs/lib"
)

// TerminalEditor manages the NCurses-based editing interface
type TerminalEditor struct {
	filePath     string
	commands     []libtrecs.Command
	backupPath   string
	currentIndex int
}

// NewTerminalEditor creates a new editor instance
func NewTerminalEditor(filePath string, commands []libtrecs.Command) *TerminalEditor {
	backupPath := filePath + ".backup." + time.Now().Format("20060102_150405")
	return &TerminalEditor{
		filePath:     filePath,
		commands:     commands,
		backupPath:   backupPath,
		currentIndex: 0,
	}
}

// Run launches the interactive editor
func (te *TerminalEditor) Run() error {
	fmt.Printf("\n=== Terminal Recording Editor ===\n\n")
	fmt.Printf("File: %s\n", te.filePath)
	fmt.Printf("Commands: %d\n\n", len(te.commands))

	// TODO: Initialize NCurses
	// For now, show a text-based preview

	te.showPreview()

	fmt.Printf("\nNCurses editor coming soon!\n")
	fmt.Printf("Backup would be saved to: %s\n", te.backupPath)

	return nil
}

// showPreview displays a preview of the commands
func (te *TerminalEditor) showPreview() {
	fmt.Printf("Command List:\n")
	fmt.Printf("=============\n\n")

	for i, cmd := range te.commands {
		fmt.Printf("[%d] Input: %s\n", i, truncate(cmd.InputText, 60))
		fmt.Printf("    Output: %s\n", truncate(cmd.OutputText, 60))
		fmt.Printf("    Time: %dms - %dms\n\n", cmd.StartTime, cmd.EndTime)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// SaveChanges writes edited commands back to the file
func (te *TerminalEditor) SaveChanges() error {
	return libtrecs.RebuildRecording(te.filePath, te.backupPath, te.commands)
}
