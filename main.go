package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/fhs/gompd/v2/mpd"
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
		"mpd/output/+/set": bridge.onMpdOutputSet,
		"mpd/pause/set":    bridge.onMpdPauseSet,
	}
	for key, function := range funcs {
		token := mqttClient.Subscribe(key, 0, function)
		token.Wait()
	}
	time.Sleep(2 * time.Second)
	bridge.initialize()
	return bridge
}

var sendMutex sync.Mutex

func (bridge *MpdMQTTBridge) onMpdOutputSet(client mqtt.Client, message mqtt.Message) {
	sendMutex.Lock()
	defer sendMutex.Unlock()

	re := regexp.MustCompile(`^mpd/output/([^/]+)/set$`)
	matches := re.FindStringSubmatch(message.Topic())
	if matches != nil {
		outputStr := matches[1]
		output, err := strconv.ParseInt(outputStr, 10, 32)
		if err != nil {
			fmt.Printf("Could not parse output '%s'\n", outputStr)
			return
		}
		p := string(message.Payload())
		if p != "" {
			enable, err := strconv.ParseBool(p)
			if err != nil {
				fmt.Printf("Could not parse %s as bool\n", p)
				return
			}
			bridge.PublishMQTT("mpd/output/"+outputStr+"/set", "", false)
			if enable {
				bridge.MPDClient.EnableOutput(int(output))
			} else {
				bridge.MPDClient.DisableOutput(int(output))
			}
		}
	}
}

func (bridge *MpdMQTTBridge) onMpdPauseSet(client mqtt.Client, message mqtt.Message) {
	sendMutex.Lock()
	defer sendMutex.Unlock()

	pause, err := strconv.ParseBool(string(message.Payload()))
	if err != nil {
		fmt.Printf("Could not parse %s as bool\n", string(message.Payload()))
		return
	}
	bridge.PublishMQTT("mpd/pause/set", "", false)
	bridge.MPDClient.Pause(pause)
}

func (bridge *MpdMQTTBridge) PublishMQTT(topic string, message string, retained bool) {
	token := bridge.MQTTClient.Publish(topic, 0, retained, message)
	token.Wait()
}

func (bridge *MpdMQTTBridge) initialize() {
	bridge.publishStatus()
	bridge.publishOutputs()
}

func (bridge *MpdMQTTBridge) publishStatus() {
	status, err := bridge.MPDClient.Status()
	if err != nil {
		log.Fatalln(err)
	} else {
		jsonStatus, err := json.Marshal(status)
		if err != nil {
			fmt.Printf("Could not serialize mpd status %v\n", err)
			return
		}
		bridge.PublishMQTT("mpd/status", string(jsonStatus), false)
	}
}

func (bridge *MpdMQTTBridge) publishOutputs() {
	outputs, err := bridge.MPDClient.ListOutputs()
	if err != nil {
		log.Fatalln(err)
	} else {
		jsonStatus, err := json.Marshal(outputs)
		if err != nil {
			fmt.Printf("Could not serialize mpd outputs %v\n", err)
			return
		}
		bridge.PublishMQTT("mpd/outputs", string(jsonStatus), false)
	}
}

func (bridge *MpdMQTTBridge) MainLoop() {
	go func() {
		for subsystem := range bridge.PlaylistWatcher.Event {
			if *debug {
				log.Printf("Event received: '%s'\n", subsystem)
			}
			if subsystem == "player" {
				bridge.publishStatus()
			} else if subsystem == "output" {
				bridge.publishOutputs()
			}
		}
	}()

	go func() {
		for {
			time.Sleep(10 * time.Second)
			err := bridge.MPDClient.Ping()
			if err != nil {
				fmt.Printf("Ping error, reconnecting %v\n", err)
				mpdClient, err := mpd.DialAuthenticated("tcp", *mpdServer, *mpdPassword)
				if err != nil {
					fmt.Printf("Error when reconnecting to MPD server: %s\n", *mpdServer)
				} else {
					bridge.MPDClient = *mpdClient
					fmt.Printf("Reconnected to MPD server: %s\n", *mpdServer)
				}

				watcher, err := mpd.NewWatcher("tcp", *mpdServer, *mpdPassword, "player", "output")
				if err != nil {
					fmt.Printf("Error when reconnecting MPD watcher: %s\n", *mpdServer)
				} else {
					bridge.PlaylistWatcher = *watcher
					fmt.Printf("Reconnected MPD watcher: %s\n", *mpdServer)
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
