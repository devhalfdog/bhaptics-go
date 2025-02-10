package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/avast/retry-go/v4"
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

	configs := []retry.Option{
		retry.OnRetry(func(attempt uint, err error) {
			log.Printf("Retry bHaptics connect Attempt %d: %s\n", attempt, err)
		}),
		retry.MaxDelay(10 * time.Second),
	}

	err := retry.Do(func() error {
		err := manager.Run()
		return err
	}, configs...)

	if err != nil {
		log.Println(err)
	}

	log.Println("bHaptics Connect")
	log.Println("aA - Get Connected Device Count")
	log.Println("sS - Is (Vest)Device Connected")
	log.Println("dD - Dot Haptic")
	log.Println("fF - Path Haptic")
	log.Println("qQ - Play Pattern from file(example.tact)")
	log.Println("wW - Queue Test1 (Pattern)")
	log.Println("eE - Queue Test2 (Pattern)")

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
			case "s": // IsDeviceConnected
				isConnected, err := manager.IsDeviceConnected(bhapticsgo.VestFrontPosition)
				if err != nil {
					log.Println(err)
				} else {
					log.Println("Is Vest Device Connected:", isConnected)
				}
			case "d": // Dot Haptic
				haptic := []bhapticsgo.HapticPoint{
					{
						Index:     0,
						Intensity: 100,
					},
					{
						Index:     1,
						Intensity: 100,
					},
				}
				err := manager.Play("Vest", bhapticsgo.PlayOption{
					Position:       bhapticsgo.VestPosition,
					DurationMillis: 1000,
					DotPoints:      haptic,
				})
				if err != nil {
					log.Println(err)
				} else {
					log.Println("Play Dot Haptic")
				}
			case "f": // Path Haptic
				haptic := []bhapticsgo.HapticPoint{
					{
						X:         0.1,
						Y:         0.1,
						Intensity: 100,
					},
					{
						X:         0.1,
						Y:         0.2,
						Intensity: 100,
					},
					{
						X:         0.1,
						Y:         0.3,
						Intensity: 100,
					},
				}
				err := manager.Play("Vest", bhapticsgo.PlayOption{
					Position:       bhapticsgo.VestPosition,
					DurationMillis: 1000,
					PathPoints:     haptic,
				})
				if err != nil {
					log.Println(err)
				} else {
					log.Println("Play Path Haptic")
				}
			case "q": // Play Pattern(example.tact)
				ok, err := manager.IsPatternRegistered("example")
				if err != nil {
					log.Println(err)
					continue
				}

				if !ok {
					err := manager.RegisterPatternFromFile("example", "./example.tact")
					if err != nil {
						log.Println(err)
						continue
					}
				}

				log.Println("Register Pattern from file(example.tact)")

				err = manager.PlayPattern("example", "alternate")
				if err != nil {
					log.Println(err)
					continue
				}

				log.Println("Play Pattern(example, alternate)")
			case "w": // Queue Test1 (Pattern)
				ok, err := manager.IsPatternRegistered("example")
				if err != nil {
					log.Println(err)
					continue
				}

				if !ok {
					err := manager.RegisterPatternFromFile("example", "./example.tact")
					if err != nil {
						log.Println(err)
						continue
					}
				}

				log.Println("Register Pattern from file(example.tact)")

				go manager.PlayPattern("example", "k1")
				go manager.PlayPattern("example", "k2")
				go manager.PlayPattern("example", "k3")
				go manager.PlayPattern("example", "k4")

				log.Println("Queue Test (Pattern)")
			case "e": // Queue Test2 (Pattern)
				ok, err := manager.IsPatternRegistered("example")
				if err != nil {
					log.Println(err)
					continue
				}

				if !ok {
					err := manager.RegisterPatternFromFile("example", "./example.tact")
					if err != nil {
						log.Println(err)
						continue
					}
				}

				log.Println("Register Pattern from file(example.tact)")

				go manager.PlayPattern("example")
				go manager.PlayPattern("example")
				go manager.PlayPattern("example")
				go manager.PlayPattern("example")

				log.Println("Queue Test2 (Pattern)")
			}
		}
	}()

	<-exit
	log.Println("Exiting...")
}
