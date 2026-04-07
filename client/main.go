package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

func main() {
	clientApp := app.NewWithID("com.fat16.analyzer.client")
	window := clientApp.NewWindow("FAT16 Analyzer Client")
	window.Resize(fyne.NewSize(900, 600))

	gui := NewGUI(clientApp, window)
	gui.Run()
}
