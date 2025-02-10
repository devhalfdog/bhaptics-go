package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	bhapticsgo "github.com/devhalfdog/bhaptics-go"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			// Handle panic here
			log.Println("Recovered in main:", r)
		}
	}()

	exit := make(chan os.Signal, 1)
	signal.Notify(exit, syscall.SIGINT, syscall.SIGTERM)

	manager := bhapticsgo.NewBHapticsManager(bhapticsgo.Option{
		// AppKey:    "string",
		// AppName:   "aaa",
		DebugMode: true,
	})

	err := manager.Run()
	if err != nil {
		log.Fatalln(err)
	}

	log.Println("bHaptics Connect")
	log.Println("aA - Get Connected Device Count")
	log.Println("sS - Is (Vest)Device Connected")

	go func() {
		for {
			var key string
			fmt.Scanln(&key)

			switch strings.ToLower(key) {
			case "a": // GetConnectedDeviceCount
				count, err := manager.GetConnectedDeviceCount()
				if err != nil {
					log.Println(err)
				} else {
					log.Println("Connected Device Count:", count)
				}
			case "s":
				isConnected, err := manager.IsDeviceConnected(bhapticsgo.VestFrontPosition)
				if err != nil {
					log.Println(err)
				} else {
					log.Println("Is Vest Device Connected:", isConnected)
				}
			case "q":
			case "d":
			case "f":
			}
		}
	}()

	<-exit
	log.Println("Exiting...")
}
