package main

import bhapticsgo "github.com/devhalfdog/bhaptics-go"

func main() {
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
