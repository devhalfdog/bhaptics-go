package main

import (
	"fmt"

	bhapticsgo "github.com/devhalfdog/bhaptics-go"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			// Handle panic here
			fmt.Println("Recovered in main:", r)
		}
	}()

	manager := bhapticsgo.NewBHapticsManager(bhapticsgo.Option{
		AppKey:    "string",
		AppName:   "aaa",
		DebugMode: true,
	})

	err := manager.Run()
	if err != nil {
		panic(err)
	}
}
