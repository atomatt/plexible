package plexible

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
)

// Time after which a subscribed controller is removed.
const controllerTimeout = time.Second * 90

type Player interface {
	Capabilities() []string
	CommandChan() chan PlayerCommand
	Subscribe() chan Timeline
}

type PlayerCommand struct {
	Type   string
	Params url.Values
}

type controller struct {
	clientID   string
	deviceName string
	url        string
	timer      *time.Timer
}

type Client struct {

	// Client details
	ID      string
	Name    string
	Product string
	Version string

	// Logger, uses the logrus StandardLogger() by default.
	Logger *logrus.Logger

	// API
	apiListener *net.TCPListener
	apiPort     int

	// Player
	player Player

	// Player timeline
	timeline      Timeline
	timelineLock  sync.Mutex
	listeners     []chan bool
	listenersLock sync.Mutex

	// Controllers
	controllers     []*controller
	controllersLock sync.Mutex

	// Discovery
	discoveryConn *net.UDPConn

	// Service cleanup channel
	shutdown chan bool
}

func NewClient(id, name, product, version string, logger *logrus.Logger) *Client {
	if logger == nil {
		logger = logrus.StandardLogger()
	}
	return &Client{
		ID:       id,
		Name:     name,
		Product:  product,
		Version:  version,
		Logger:   logger,
		shutdown: make(chan bool),
	}
}

func (c *Client) AddPlayer(p Player) {
	c.player = p
	ch := p.Subscribe()
	go func() {
		c.Logger.Debugf("player %v timeline subscription started", p)
		defer c.Logger.Errorf("player %v timeline subscription ended", p)
		for {
			t, ok := <-ch
			if !ok {
				return
			}
			c.updateTimeline(p, t)
		}
	}()
}

func (c *Client) updateTimeline(p Player, t Timeline) {
	c.timelineLock.Lock()
	defer c.timelineLock.Unlock()
	c.Logger.Debugf("timeline %v from player %v", t, p)
	c.timeline = t
	c.wakeListeners()
	c.notifyControllers()
}

func (c *Client) Start() error {

	if c.player == nil {
		return errors.New("cannot start: no players added")
	}

	// Start services.
	err := startClientAPI(c)
	if err != nil {
		return fmt.Errorf("error starting api (%s)", err)
	}
	err = startClientDiscovery(c)
	if err != nil {
		return fmt.Errorf("error starting discovery (%s)", err)
	}

	// Say hello.
	err = hello(c)
	if err != nil {
		return fmt.Errorf("error saying hello (%s)", err)
	}

	return nil
}

func (c *Client) Stop() error {
	bye(c)
	c.discoveryConn.Close()
	<-c.shutdown
	c.apiListener.Close()
	<-c.shutdown
	return nil
}

func startClientAPI(c *Client) error {

	api := http.NewServeMux()

	api.HandleFunc("/resources", func(w http.ResponseWriter, r *http.Request) {
		players := []player{
			{
				Title:                c.Name,
				MachineIdentifier:    c.ID,
				Product:              c.Product,
				Version:              c.Version,
				ProtocolVersion:      "1",
				ProtocolCapabilities: strings.Join(c.player.Capabilities(), ","),
				DeviceClass:          "htpc",
			},
		}
		msg, _ := xml.Marshal(MediaContainer{Players: players})
		c.Logger.Debugf("sending resources response: %q", msg)
		w.Header().Add("Content-Type", "text/xml; charset=utf-8")
		w.Write([]byte(msg))
	})

	api.HandleFunc("/player/timeline/poll", func(w http.ResponseWriter, r *http.Request) {

		commandID := r.FormValue("commandID")
		wait := r.FormValue("wait") == "1"

		// Block until there's a timeline update or the timeout expires.
		if wait {
			c.Logger.Debugf("waiting for timeline update")
			ch := make(chan bool)
			c.addListener(ch)
			defer c.removeListener(ch)
			select {
			case <-ch:
			case <-time.After(time.Second * 30):
			}
		}

		c.timelineLock.Lock()
		defer c.timelineLock.Unlock()

		// TODO: track/cache player timelines and just send
		mc := MediaContainer{
			CommandID:         commandID,
			MachineIdentifier: c.ID,
			Timelines:         []Timeline{c.timeline},
		}

		msg, _ := xml.Marshal(mc)
		c.Logger.Debugf("poll response: %q", msg)

		w.Header().Add("Access-Control-Allow-Origin", "*")
		w.Header().Add("Access-Control-Expose-Headers", "X-Plex-Client-Identifier")
		w.Header().Add("X-Plex-Client-Identifier", c.ID)
		w.Header().Add("X-Plex-Protocol", "1.0")
		w.Header().Add("Content-Type", "text/xml; charset=utf-8")
		w.Write(msg)
	})

	api.HandleFunc("/player/playback/", func(w http.ResponseWriter, r *http.Request) {
		// Parse form and URL.
		cmdType := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		r.ParseForm()
		// Send command to player.
		c.player.CommandChan() <- PlayerCommand{Type: cmdType, Params: r.Form}
		w.WriteHeader(200)
	})

	api.HandleFunc("/player/timeline/subscribe", func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		c.addController(
			fmt.Sprintf("%s://%s:%s/", r.FormValue("protocol"), host, r.FormValue("port")),
			r.Header.Get("X-Plex-Client-Identifier"),
			r.Header.Get("X-Plex-Device-Name"),
		)
	})

	api.HandleFunc("/player/timeline/unsubscribe", func(w http.ResponseWriter, r *http.Request) {
		c.removeController(r.Header.Get("X-Plex-Client-Identifier"))
	})

	optionsWrapper := func(w http.ResponseWriter, r *http.Request) {
		c.Logger.Debug(r.Method, r.URL.Path)
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

func (c *Client) addListener(ch chan bool) {
	c.listenersLock.Lock()
	defer c.listenersLock.Unlock()
	c.listeners = append(c.listeners, ch)
}

func (c *Client) removeListener(ch chan bool) {
	c.listenersLock.Lock()
	defer c.listenersLock.Unlock()
	var ls []chan bool
	for _, l := range c.listeners {
		if l != ch {
			ls = append(ls, l)
		}
	}
	c.listeners = ls
}

func (c *Client) wakeListeners() {
	c.listenersLock.Lock()
	defer c.listenersLock.Unlock()
	for _, ch := range c.listeners {
		ch <- true
	}
}

func (c *Client) addController(url, clientID, deviceName string) {
	c.controllersLock.Lock()
	defer c.controllersLock.Unlock()
	// Existing controller ... reset its timer.
	for _, cntrllr := range c.controllers {
		if cntrllr.clientID == clientID {
			c.Logger.Debugf("resetting timer for controller %s [%s]", deviceName, clientID)
			updateControllerTimer(c, cntrllr)
			return
		}
	}
	// New controller ... add to list.
	c.Logger.Debugf("adding controller %s [%s]", deviceName, clientID)
	cntrllr := &controller{clientID: clientID, deviceName: deviceName, url: url}
	updateControllerTimer(c, cntrllr)
	c.controllers = append(c.controllers, cntrllr)
}

func (c *Client) removeController(clientID string) {
	c.controllersLock.Lock()
	defer c.controllersLock.Unlock()
	controllers := []*controller{}
	for _, cntrllr := range c.controllers {
		if cntrllr.clientID == clientID {
			c.Logger.Debugf("removing controller %s [%s]", cntrllr.deviceName, cntrllr.clientID)
			cntrllr.timer.Stop()
		} else {
			controllers = append(controllers, cntrllr)
		}
	}
	c.controllers = controllers
}

func updateControllerTimer(c *Client, cntrllr *controller) {
	if cntrllr.timer != nil {
		cntrllr.timer.Reset(controllerTimeout)
		return
	}
	cntrllr.timer = time.AfterFunc(controllerTimeout, func() {
		c.removeController(cntrllr.clientID)
	})
}

func (c *Client) notifyControllers() {
	c.controllersLock.Lock()
	defer c.controllersLock.Unlock()
	for _, cntrllr := range c.controllers {
		c.Logger.Debugf("sending timeline to controller %s [%s]",
			cntrllr.deviceName, cntrllr.clientID)

		mc := MediaContainer{
			MachineIdentifier: c.ID,
			Timelines:         []Timeline{c.timeline},
		}
		buf, err := xml.Marshal(mc)
		if err != nil {
			c.Logger.Error("error encoding controller timeline xml: %s", err)
			return
		}

		req, err := http.NewRequest("POST", cntrllr.url+":/timeline", bytes.NewReader(buf))
		if err != nil {
			c.Logger.Error("error creating request: %s", err)
			return
		}
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("X-Plex-Client-Identifier", c.ID)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			c.Logger.Errorf("error posting timeline to device %s [%s]: %s",
				cntrllr.deviceName, cntrllr.clientID, err)
			return
		}
		defer resp.Body.Close()

		c.Logger.Debugf("timeline post to %s [%s] returned %s",
			cntrllr.deviceName, cntrllr.clientID, resp.Status)
	}
}

func startClientDiscovery(c *Client) error {

	c.Logger.Infof("listening for client discovery requests on port %d", clientDiscoveryPort)
	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.ParseIP(discoveryIP),
		Port: clientDiscoveryPort,
	})
	if err != nil {
		return fmt.Errorf("error creating discovery socket (%s)", err)
	}
	c.discoveryConn = conn

	go func() {
		defer func() {
			c.Logger.Info("client discovery loop ending")
			c.shutdown <- true
		}()

		c.Logger.Info("client discovery loop running")
		for {
			b := make([]byte, 1024)
			_, addr, err := c.discoveryConn.ReadFrom(b)
			if err != nil {
				return
			}

			msg := clientMsg("HTTP/1.0 200 OK", c)
			c.Logger.Debugf("client discovery request from %s", addr)
			c.Logger.Debugf("sending client discovery response: %q", msg)
			c.discoveryConn.WriteTo(msg, addr)
		}
	}()
	return nil
}

func hello(c *Client) error {
	c.Logger.Info("announcing player to network")
	msg := clientMsg("HELLO * HTTP/1.0", c)
	c.Logger.Debugf("HELLO: %q", msg)
	_, err := c.discoveryConn.WriteTo(msg, &net.UDPAddr{
		IP:   net.ParseIP(discoveryIP),
		Port: clientBroadcastPort,
	})
	if err != nil {
		return fmt.Errorf("error sending HELLO (%s)", err)
	}
	return nil
}

func bye(c *Client) error {
	c.Logger.Info("removing player from network")
	msg := clientMsg("BYE * HTTP/1.0", c)
	c.Logger.Debugf("BYE: %q", msg)
	_, err := c.discoveryConn.WriteTo(msg, &net.UDPAddr{
		IP:   net.ParseIP(discoveryIP),
		Port: clientBroadcastPort,
	})
	if err != nil {
		return fmt.Errorf("error sending BYE (%s)", err)
	}
	return nil
}

func clientMsg(header string, c *Client) []byte {

	params := map[string]string{
		"Content-Type":          "plex/media-player",
		"Name":                  c.Name,
		"Port":                  strconv.Itoa(c.apiPort),
		"Product":               c.Product,
		"Protocol":              "plex",
		"Protocol-Version":      "1",
		"Protocol-Capabilities": "timeline,playback",
		"Resource-Identifier":   c.ID,
		"Version":               c.Version,
	}

	w := bytes.NewBuffer(nil)
	w.WriteString(header)
	for k, v := range params {
		w.WriteString("\n")
		w.WriteString(k)
		w.WriteString(": ")
		w.WriteString(v)
	}

	return w.Bytes()
}

/*
TODO:
	* get resources info from player(s)
	* proper XML handling, everywhere
	* multiple typed players?
	* create/reuse xml encoder for connection
*/
