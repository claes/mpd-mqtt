package lib

import (
	"encoding/json"
	"log"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/fhs/gompd/v2/mpd"
)

type MpdMQTTBridge struct {
	MQTTClient      mqtt.Client
	MPDClient       mpd.Client
	PlaylistWatcher mpd.Watcher
	TopicPrefix     string
}

func CreateMPDClient(mpdServer, mpdPassword string) (*mpd.Client, *mpd.Watcher, error) {
	mpdClient, err := mpd.DialAuthenticated("tcp", mpdServer, mpdPassword)
	if err != nil {
		slog.Error("Could not connect to MPD server", "mpdServer", mpdServer, "error", err)
		return mpdClient, nil, err
	} else {
		slog.Info("Connected to MPD server", "mpdServer", mpdServer)
	}

	watcher, err := mpd.NewWatcher("tcp", mpdServer, mpdPassword, "player", "output")
	if err != nil {
		slog.Error("Could not create MPD watcher", "mpdServer", mpdServer, "error", err)
		return mpdClient, watcher, err
	} else {
		slog.Info("Created MPD watcher", "mpdServer", mpdServer)
	}
	return mpdClient, watcher, nil
}

func CreateMQTTClient(mqttBroker string) mqtt.Client {
	slog.Info("Creating MQTT client", "broker", mqttBroker)
	opts := mqtt.NewClientOptions().AddBroker(mqttBroker)
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		slog.Error("Could not connect to broker", "mqttBroker", mqttBroker, "error", token.Error())
		panic(token.Error())
	}
	slog.Info("Connected to MQTT broker", "mqttBroker", mqttBroker)
	return client
}

func NewMpdMQTTBridge(mpdClient *mpd.Client, watcher *mpd.Watcher, mqttClient mqtt.Client, topicPrefix string) *MpdMQTTBridge {

	bridge := &MpdMQTTBridge{
		MQTTClient:      mqttClient,
		MPDClient:       *mpdClient,
		PlaylistWatcher: *watcher,
		TopicPrefix:     topicPrefix,
	}

	funcs := map[string]func(client mqtt.Client, message mqtt.Message){
		"mpd/output/+/set": bridge.onMpdOutputSet,
		"mpd/pause/set":    bridge.onMpdPauseSet,
	}
	for key, function := range funcs {
		token := mqttClient.Subscribe(topicPrefix+"/"+key, 0, function)
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
			slog.Error("Could not parse output as int", "output", outputStr, "error", err)
			return
		}
		p := string(message.Payload())
		if p != "" {
			enable, err := strconv.ParseBool(p)
			if err != nil {
				slog.Error("Could not parse bool", "payload", p, "error", err)
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
		slog.Error("Could not parse bool", "payload", message.Payload(), "error", err)
		return
	}
	bridge.PublishMQTT("mpd/pause/set", "", false)
	bridge.MPDClient.Pause(pause)
}

func (bridge *MpdMQTTBridge) PublishMQTT(subtopic string, message string, retained bool) {
	token := bridge.MQTTClient.Publish(bridge.TopicPrefix+"/"+subtopic, 0, retained, message)
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
			slog.Error("Could not serialize mpd status", "status", status, "error", err)
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
			slog.Error("Could not serialize mpd outputs", "outputs", outputs, "error", err)
			return
		}
		bridge.PublishMQTT("mpd/outputs", string(jsonStatus), false)
	}
}

func (bridge *MpdMQTTBridge) MainLoop() {
	for subsystem := range bridge.PlaylistWatcher.Event {
		slog.Debug("Event received", "subsystem", subsystem)
		if subsystem == "player" {
			bridge.publishStatus()
		} else if subsystem == "output" {
			bridge.publishOutputs()
		}
	}
}

func (bridge *MpdMQTTBridge) DetectReconnectMPDClient(mpdServer, mpdPassword string) {
	for {
		time.Sleep(10 * time.Second)
		err := bridge.MPDClient.Ping()
		if err != nil {
			slog.Error("Ping error, reconnecting", "error", err)
			mpdClient, watcher, err := CreateMPDClient(mpdServer, mpdPassword)
			if err == nil {
				bridge.MPDClient = *mpdClient
				bridge.PlaylistWatcher = *watcher
				slog.Error("Reconnected")
			} else {
				slog.Error("Ping when reconnecting", "error", err)
			}
		}
	}
}
