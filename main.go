package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/claes/mpd-mqtt/lib"
)

var (
	mpdServer   *string
	mpdPassword *string
	mqttBroker  *string
	help        *bool
	debug       *bool
)

func init() {
	mpdServer = flag.String("mpd-address", "localhost:6600", "MPD Server address and port")
	mpdPassword = flag.String("mpd-password", "", "MPD password (optional)")
	mqttBroker = flag.String("broker", "tcp://localhost:1883", "MQTT broker URL")
	help = flag.Bool("help", false, "Print help")
	debug = flag.Bool("debug", false, "Debug logging")
}

func printHelp() {
	fmt.Println("Usage: mpd-mqtt [OPTIONS]")
	fmt.Println("Options:")
	flag.PrintDefaults()
}

func main() {
	flag.Parse()

	if *help {
		printHelp()
		os.Exit(0)
	}

	mpdClient, watcher, err := lib.CreateMPDClient(*mpdServer, *mpdPassword)
	if err != nil {
		fmt.Printf("Error creating MPD client, %v \n", err)
		os.Exit(1)
	}
	bridge := lib.NewMpdMQTTBridge(mpdClient, watcher,
		lib.CreateMQTTClient(*mqttBroker))

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	fmt.Printf("Started\n")
	go bridge.MainLoop(*mpdServer, *mpdPassword)
	<-c
	bridge.PlaylistWatcher.Close()
	fmt.Printf("Shut down\n")

	os.Exit(0)
}
