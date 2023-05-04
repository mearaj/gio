package main

import (
	"fmt"
	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"
	"log"
	"os"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

func main() {
	go func() {
		w := app.NewWindow()
		if err := loop(w); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func loop(w *app.Window) error {
	var ops op.Ops
	for {
		select {
		case e := <-w.Events():
			switch e := e.(type) {
			case system.DestroyEvent:
				return e.Err
			case system.FrameEvent:
				gtx := layout.NewContext(&ops, e)
				Layout(gtx)
				e.Frame(gtx.Ops)
			}
		}
	}
}

var list layout.List
var listLength = 30
var th = material.NewTheme(gofont.Collection())

func Layout(gtx C) D {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return list.Layout(gtx, listLength, func(gtx C, index int) D {
		if listLength < index+2 {
			listLength += 10
		}
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.UniformInset(unit.Dp(8)).Layout(gtx, material.Body1(th, fmt.Sprintf("Item %d", index)).Layout)
			}),
		)
	})
}
