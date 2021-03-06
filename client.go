package plexible

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
)

// Time after which a subscribed controller is removed.
const controllerTimeout = time.Second * 90

// ClientInfo contains static information about the client.
type ClientInfo struct {
	ID      string
	Name    string
	Product string
	Version string
}

// playerInfo holds info about a registered player and its current state.
type playerInfo struct {
	Type         string
	Capabilities []string
	Timeline     *PlayerTimeline
	Timelines    <-chan *PlayerTimeline
	Cmds         chan<- interface{}
}

// A controller is a device that controls the client. It is either polling
// (typically a web client) or subscribing (other types of client).
type controller interface {
	fmt.Stringer
	ClientID() string
	Send(clientID string, mc *MediaContainer) error
}

// A registeredController tracks an attached controller and its state.
type registeredController struct {
	controller controller
	timeout    *time.Timer
	commandID  string
}

// A subscribingController is a device that explicitly subscribes to and
// unsubscribes from this client. Timeline updates and posted to the
// controller's HTTP API.
type subscribingController struct {
	clientID string
	url      string
}

func (c *subscribingController) ClientID() string {
	return c.clientID
}

func (c *subscribingController) String() string {
	return fmt.Sprintf("%s at %s", c.clientID, c.url)
}

func (c *subscribingController) Send(clientID string, mc *MediaContainer) error {

	buf, err := xml.Marshal(mc)
	if err != nil {
		return fmt.Errorf("error encoding xml: %s", err)
	}

	req, err := http.NewRequest("POST", c.url+":/timeline", bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("error creating request: %s", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("X-Plex-Client-Identifier", clientID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("error performing request: %s", err)
	}
	defer resp.Body.Close()

	return nil
}

// A polling controller, e.g. the standard web client, uses long polling to get
// rapid timeline updates. The client's request handler is expected to block
// until there's an update and sends the new timeline as the response body.
type pollingController struct {
	clientID string
	ch       chan *MediaContainer
}

func (c *pollingController) String() string {
	return c.clientID
}

func (c *pollingController) ClientID() string {
	return c.clientID
}

func (c *pollingController) Send(clientID string, mc *MediaContainer) error {
	c.ch <- mc
	close(c.ch)
	return nil
}

// Client implements the core of a Plex client device. It handles discovery,
// controller subscriptions, player state tracking, etc.
type Client struct {

	// Client details
	Info *ClientInfo

	// Logger, uses the logrus StandardLogger() by default.
	Logger *logrus.Logger

	// API
	apiListener *net.TCPListener
	apiPort     int

	// Player
	players     []*playerInfo
	playersLock sync.Mutex

	// Controllers
	controllers     []*registeredController
	controllersLock sync.Mutex

	// Discovery
	discovery     *ClientDiscovery
	discoveryConn *net.UDPConn

	// Service cleanup channel
	shutdown chan bool
}

func NewClient(info *ClientInfo, logger *logrus.Logger) *Client {
	if logger == nil {
		logger = logrus.StandardLogger()
	}
	return &Client{
		Info:     info,
		Logger:   logger,
		shutdown: make(chan bool),
	}
}

func (c *Client) AddPlayer(playerType string, capabilities []string,
	timelines <-chan *PlayerTimeline, cmds chan<- interface{}) {
	c.playersLock.Lock()
	defer c.playersLock.Unlock()
	p := &playerInfo{playerType, capabilities, nil, timelines, cmds}
	c.players = append(c.players, p)
	go func() {
		c.Logger.Debugf("player %v timeline subscription started", playerType)
		defer c.Logger.Errorf("player %v timeline subscription ended", playerType)
		for {
			if t, ok := <-timelines; ok {
				c.Logger.Debugf("timeline %v from player %v", t, playerType)
				p.Timeline = t
				c.notifyControllers()
			} else {
				return
			}
		}
	}()
}

func (c *Client) Start() error {

	if c.players == nil {
		return errors.New("cannot start: no players added")
	}

	// Start services.
	err := startClientAPI(c)
	if err != nil {
		return fmt.Errorf("error starting api (%s)", err)
	}
	err = c.startClientDiscovery()
	if err != nil {
		return fmt.Errorf("error starting api (%s)", err)
	}

	err = c.discovery.Hello(nil)
	if err != nil {
		return fmt.Errorf("error sending hello (%s)", err)
	}

	return nil
}

func (c *Client) Stop() error {
	c.discoveryConn.Close()
	c.discovery.Bye(nil)
	c.apiListener.Close()
	<-c.shutdown
	return nil
}

func startClientAPI(c *Client) error {

	api := http.NewServeMux()

	api.HandleFunc("/resources", func(w http.ResponseWriter, r *http.Request) {
		players := make([]player, len(c.players))
		for _, p := range c.players {
			players = append(players, player{
				Title:                c.Info.Name,
				MachineIdentifier:    c.Info.ID,
				Product:              c.Info.Product,
				Version:              c.Info.Version,
				ProtocolVersion:      "1",
				ProtocolCapabilities: strings.Join(p.Capabilities, ","),
				DeviceClass:          "htpc",
			})
		}
		msg, _ := xml.Marshal(MediaContainer{Players: players})
		c.Logger.Debugf("sending resources response: %q", msg)
		w.Header().Add("Content-Type", "text/xml; charset=utf-8")
		w.Write([]byte(msg))
	})

	api.HandleFunc("/player/timeline/poll", func(w http.ResponseWriter, r *http.Request) {

		controllerID := r.Header.Get("X-Plex-Client-Identifier")
		commandID := r.FormValue("commandID")
		wait := r.FormValue("wait") == "1"

		var mc *MediaContainer

		// Block until there's a timeline update or the timeout expires.
		if wait {
			c.Logger.Debugf("waiting for timeline update")
			ch := make(chan *MediaContainer)
			rc := c.registerPollingController(controllerID, ch, commandID)
			defer c.forgetController(controllerID)
			select {
			case mc = <-ch:
				commandID = rc.commandID
			case <-time.After(time.Second * 30):
			}
		}

		if mc == nil {
			t := c.collectTimelines()
			mc = makeTimeline(c.Info.ID, commandID, t)
		}

		msg, _ := xml.Marshal(mc)
		c.Logger.Debugf("poll response: %q", msg)

		w.Header().Add("Access-Control-Allow-Origin", "*")
		w.Header().Add("Access-Control-Expose-Headers", "X-Plex-Client-Identifier")
		w.Header().Add("X-Plex-Client-Identifier", c.Info.ID)
		w.Header().Add("X-Plex-Protocol", "1.0")
		w.Header().Add("Content-Type", "text/xml; charset=utf-8")
		w.Write(msg)
	})

	api.HandleFunc("/player/playback/playMedia", func(w http.ResponseWriter, r *http.Request) {

		controllerID := r.Header.Get("X-Plex-Client-Identifier")
		commandID := r.FormValue("commandID")
		c.updateControllerCommandID(controllerID, commandID)

		containerKey := r.FormValue("containerKey")
		key := r.FormValue("key")
		offset, _ := strconv.ParseUint(r.FormValue("offset"), 10, 64)

		serverURL := fmt.Sprintf("%s://%s:%s", r.FormValue("protocol"),
			r.FormValue("address"), r.FormValue("port"))
		url := fmt.Sprintf("%s%s", serverURL, containerKey)

		c.Logger.Debugf("fetching play media from %s", url)
		mc := &MediaContainer{}
		err := getXML(url, mc)
		if err != nil {
			c.Logger.Errorf("error retrieving media container from %s (%s)", url, err)
			// TODO: return error
			return
		}

		var playerType string
		switch {
		case mc.Tracks != nil:
			playerType = TypeMusic
		default:
			c.Logger.Errorf("can't determine type of player")
			// TODO: return error
			return
		}

		player := c.playerForType(playerType)
		if player == nil {
			c.Logger.Warnf("no player for type %s", playerType)
			// TODO: return error
			return
		}
		player.Cmds <- &PlayMediaCommand{
			serverURL,
			mc,
			containerKey,
			key,
			offset,
		}
	})

	api.HandleFunc("/player/playback/", func(w http.ResponseWriter, r *http.Request) {

		controllerID := r.Header.Get("X-Plex-Client-Identifier")
		commandID := r.FormValue("commandID")
		c.updateControllerCommandID(controllerID, commandID)

		cmdType := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var cmd interface{}
		switch cmdType {
		case "pause":
			cmd = &PauseCommand{}
		case "play":
			cmd = &PlayCommand{}
		case "stop":
			cmd = &StopCommand{}
		default:
			c.Logger.Warnf("unrecognised player command %s", cmdType)
			// TODO: return error
			return
		}

		playerType := r.FormValue("type")
		player := c.playerForType(playerType)
		if player == nil {
			c.Logger.Warnf("no player for type %s", playerType)
			// TODO: return error
			return
		}

		player.Cmds <- cmd
	})

	api.HandleFunc("/player/timeline/subscribe", func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		controllerID := r.Header.Get("X-Plex-Client-Identifier")
		commandID := r.FormValue("commandID")
		rc := c.registerSubscribingController(
			controllerID,
			fmt.Sprintf("%s://%s:%s/", r.FormValue("protocol"), host, r.FormValue("port")),
			commandID,
		)
		c.SendTimeline(rc, c.collectTimelines())
	})

	api.HandleFunc("/player/timeline/unsubscribe", func(w http.ResponseWriter, r *http.Request) {
		controllerID := r.Header.Get("X-Plex-Client-Identifier")
		c.forgetController(controllerID)
	})

	optionsWrapper := func(w http.ResponseWriter, r *http.Request) {
		c.Logger.Debugf("%s %s", r.Method, r.URL.Path)
		if r.Method == "OPTIONS" {
			w.Header().Add("Access-Control-Allow-Headers", "x-plex-version, x-plex-platform-version, x-plex-username, x-plex-client-identifier, x-plex-target-client-identifier, x-plex-device-name, x-plex-platform, x-plex-product, accept-language, accept, x-plex-device")
			w.Header().Add("Access-Control-Allow-Origin", "*")
			w.Header().Add("Access-Control-Allow-Methods", "POST, GET, OPTIONS, DELETE, PUT, HEAD")
			w.WriteHeader(200)
		} else {
			api.ServeHTTP(w, r)
		}
	}

	l, err := net.ListenTCP("tcp", nil)
	if err != nil {
		return fmt.Errorf("error creating api socket (%s)", err)
	}
	c.apiListener = l

	_, port, _ := net.SplitHostPort(l.Addr().String())
	c.apiPort, _ = strconv.Atoi(port)

	go func() {
		c.Logger.Infof("client API listening on %s", c.apiListener.Addr())
		http.Serve(l, http.HandlerFunc(optionsWrapper))
		c.Logger.Info("client api shutting down")
		c.shutdown <- true
	}()

	return nil
}

func getXML(url string, v interface{}) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	err = xml.NewDecoder(resp.Body).Decode(v)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) startClientDiscovery() error {

	discoveryConn, err := net.ListenUDP("udp", &StandardClientDiscoveryAddr)
	if err != nil {
		return fmt.Errorf("error creating discovery udp socket (%s)", err)
	}

	c.discoveryConn = discoveryConn
	c.discovery = &ClientDiscovery{c.Info, c.apiPort, c.Logger}
	go c.discovery.Serve(c.discoveryConn)

	return nil
}

func (c *Client) updateControllerCommandID(clientID, commandID string) {
	c.controllersLock.Lock()
	defer c.controllersLock.Unlock()
	for _, rc := range c.controllers {
		if rc.controller.ClientID() == clientID {
			rc.commandID = commandID
			break
		}
	}
}

func (c *Client) playerForType(t string) *playerInfo {
	c.playersLock.Lock()
	defer c.playersLock.Unlock()
	for _, p := range c.players {
		if p.Type == t {
			return p
		}
	}
	return nil
}

func (c *Client) collectTimelines() []Timeline {
	c.playersLock.Lock()
	defer c.playersLock.Unlock()
	t := make([]Timeline, 0, len(c.players))
	for _, p := range c.players {
		if p.Timeline != nil {
			t = append(t, Timeline{PlayerTimeline: p.Timeline, Type: p.Type})
		}
	}
	return t
}

func (c *Client) registerSubscribingController(clientID, url, commandID string) *registeredController {
	c.controllersLock.Lock()
	defer c.controllersLock.Unlock()

	// Existing controller ... reset its timeout and update command ID.
	for _, rc := range c.controllers {
		if rc.controller.ClientID() == clientID {
			c.Logger.Debugf("resetting timeout for subscribing controller %s", clientID)
			rc.timeout.Reset(controllerTimeout)
			rc.commandID = commandID
			return rc
		}
	}

	// New controller ... add to list.
	c.Logger.Infof("adding subscribing controller %s", clientID)
	rc := &registeredController{
		&subscribingController{clientID: clientID, url: url},
		time.AfterFunc(controllerTimeout, func() {
			c.forgetController(clientID)
		}),
		commandID,
	}
	c.controllers = append(c.controllers, rc)
	return rc
}

func (c *Client) registerPollingController(clientID string, ch chan *MediaContainer, commandID string) *registeredController {
	c.controllersLock.Lock()
	defer c.controllersLock.Unlock()
	c.Logger.Infof("adding polling controller %s", clientID)
	rc := &registeredController{
		controller: &pollingController{clientID: clientID, ch: ch},
		commandID:  commandID,
	}
	c.controllers = append(c.controllers, rc)
	return rc
}

func (c *Client) forgetController(clientID string) {
	c.controllersLock.Lock()
	defer c.controllersLock.Unlock()
	for i, rc := range c.controllers {
		if rc.controller.ClientID() == clientID {
			c.Logger.Infof("forgetting controller %s", clientID)
			c.controllers = append(c.controllers[:i], c.controllers[i+1:]...)
			if rc.timeout != nil {
				rc.timeout.Stop()
			}
			break
		}
	}
}

func (c *Client) notifyControllers() {
	c.controllersLock.Lock()
	defer c.controllersLock.Unlock()
	t := c.collectTimelines()
	for _, rc := range c.controllers {
		c.SendTimeline(rc, t)
	}
}

func (c *Client) SendTimeline(rc *registeredController, t []Timeline) error {
	c.Logger.Debugf("sending timeline to %s", rc.controller.String())
	err := rc.controller.Send(c.Info.ID, makeTimeline(c.Info.ID, rc.commandID, t))
	if err != nil {
		c.Logger.Errorf("error sending timeline to controller %s: %s",
			rc.controller.ClientID(), err)
	}
	return err
}

func makeTimeline(clientID, commandID string, timeline []Timeline) *MediaContainer {
	return &MediaContainer{
		MachineIdentifier: clientID,
		CommandID:         commandID,
		Timelines:         timeline,
	}
}

/*
TODO:
	* proper XML handling, everywhere
	* create/reuse xml encoder for connection
*/
