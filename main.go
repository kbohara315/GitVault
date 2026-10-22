package main

import (
	"log"

	"gitvault/internal/desktop"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	app := desktop.New()
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:     "GitVault",
			Width:     1000,
			Height:    700,
			MinWidth:  800,
			MinHeight: 560,
			StateKey:  "main",
			Content:   ui.View(app.View),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
