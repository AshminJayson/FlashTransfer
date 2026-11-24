// FlashTransfer: desktop app to copy files between a Mac and an Android phone.
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[ProgressEvent]("progress")
	application.RegisterEvent[[]string]("files-dropped")
	application.RegisterEvent[ConnectStatusEvent]("connect-status")
	application.RegisterEvent[string]("log")
}

func main() {
	logs := &LogService{}
	log.SetOutput(logs.writer())
	log.SetFlags(log.Ltime)

	app := application.New(application.Options{
		Name:        "FlashTransfer",
		Description: "Copy files between a Mac and an Android phone",
		Services:    []application.Service{application.NewService(&PhoneService{}), application.NewService(logs)},
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac:         application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:          "FlashTransfer",
		Width:          980,
		Height:         640,
		MinWidth:       720,
		MinHeight:      460,
		EnableFileDrop: true,
		URL:            "/",
	})
	win.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		app.Event.Emit("files-dropped", e.Context().DroppedFiles())
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
