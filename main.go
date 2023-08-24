package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/fhs/gompd/v2/mpd"
)

var debug *bool

type MpdMQTTBridge struct {
	MQTTClient      mqtt.Client
	MPDClient       mpd.Client
	PlaylistWatcher mpd.Watcher
}

func NewMpdMQTTBridge(mpdServer string, mpdPassword string, mqttBroker string) *MpdMQTTBridge {

	opts := mqtt.NewClientOptions().AddBroker(mqttBroker)
	mqttClient := mqtt.NewClient(opts)
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	} else if *debug {
		fmt.Printf("Connected to MQTT broker: %s\n", mqttBroker)
	}

	mpdClient, err := mpd.DialAuthenticated("tcp", mpdServer, mpdPassword)
	if err != nil {
		panic(err)
	} else if *debug {
		fmt.Printf("Connected to MPD server: %s\n", mpdServer)
	}

	watcher, err := mpd.NewWatcher("tcp", mpdServer, mpdPassword, "player", "output")
	if err != nil {
		panic(err)
	}

	bridge := &MpdMQTTBridge{
		MQTTClient:      mqttClient,
		MPDClient:       *mpdClient,
		PlaylistWatcher: *watcher,
	}

	funcs := map[string]func(client mqtt.Client, message mqtt.Message){
		"mpd/test": bridge.onMpdTest,
	}
	for key, function := range funcs {
		token := mqttClient.Subscribe(key, 0, function)
		token.Wait()
	}
	time.Sleep(2 * time.Second)
	return bridge
}

var sendMutex sync.Mutex

func (bridge *MpdMQTTBridge) onMpdTest(client mqtt.Client, message mqtt.Message) {
	sendMutex.Lock()
	defer sendMutex.Unlock()

	defaultSink := string(message.Payload())
	if defaultSink != "" {
		bridge.PublishMQTT("mpd/test", "", false)
		bridge.MPDClient.Ping()
	}
}

func (bridge *MpdMQTTBridge) PublishMQTT(topic string, message string, retained bool) {
	token := bridge.MQTTClient.Publish(topic, 0, retained, message)
	token.Wait()
}

func (bridge *MpdMQTTBridge) MainLoop() {
	go func() {
		for subsystem := range bridge.PlaylistWatcher.Event {
			if *debug {
				log.Printf("Event '%s':\n", subsystem)
			}
			if subsystem == "player" {
				status, err := bridge.MPDClient.Status()
				if err != nil {
					log.Fatalln(err)
				} else {
					jsonStatus, err := json.Marshal(status)
					if err != nil {
						fmt.Printf("Could not serialize mpd status %v\n", err)
						continue
					}
					bridge.PublishMQTT("mpd/status", string(jsonStatus), false)
				}
			} else if subsystem == "output" {
				outputs, err := bridge.MPDClient.ListOutputs()
				if err != nil {
					log.Fatalln(err)
				} else {
					jsonStatus, err := json.Marshal(outputs)
					if err != nil {
						fmt.Printf("Could not serialize mpd outputs %v\n", err)
						continue
					}
					bridge.PublishMQTT("mpd/outputs", string(jsonStatus), false)
				}
			}
		}
	}()
}

func printHelp() {
	fmt.Println("Usage: mpd-mqtt [OPTIONS]")
	fmt.Println("Options:")
	flag.PrintDefaults()
}

func main() {
	mpdServer := flag.String("mpd-address", "localhost:6600", "MPD Server address and port")
	mpdPassword := flag.String("mpd-password", "", "MPD password (optional)")
	mqttBroker := flag.String("broker", "tcp://localhost:1883", "MQTT broker URL")
	help := flag.Bool("help", false, "Print help")
	debug = flag.Bool("debug", false, "Debug logging")
	flag.Parse()

	if *help {
		printHelp()
		os.Exit(0)
	}

	bridge := NewMpdMQTTBridge(*mpdServer, *mpdPassword, *mqttBroker)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	fmt.Printf("Started\n")
	go bridge.MainLoop()
	<-c
	bridge.PlaylistWatcher.Close()
	fmt.Printf("Shut down\n")

	os.Exit(0)
}
