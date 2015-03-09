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

// A controller is a device that is controlling this client. It is either
// polling (typically a web client) or subscribing (other types of client).
type controller interface {
	fmt.Stringer
	ClientID() string
	Send(clientID string, mc *MediaContainer) error
}

// A registeredController tracks an attached controller's state.
type registeredController struct {
	controller controller
	timeout    *time.Timer
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
	listeners     []chan struct{}
	listenersLock sync.Mutex

	// Controllers
	registeredControllers     []*registeredController
	registeredControllersLock sync.Mutex

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
			ch := make(chan struct{})
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
		rc := c.registerSubscribingController(
			r.Header.Get("X-Plex-Client-Identifier"),
			fmt.Sprintf("%s://%s:%s/", r.FormValue("protocol"), host, r.FormValue("port")),
		)
		c.SendTimeline(rc)
	})

	api.HandleFunc("/player/timeline/unsubscribe", func(w http.ResponseWriter, r *http.Request) {
		c.forgetController(r.Header.Get("X-Plex-Client-Identifier"))
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

func (c *Client) addListener(ch chan struct{}) {
	c.listenersLock.Lock()
	defer c.listenersLock.Unlock()
	c.listeners = append(c.listeners, ch)
}

func (c *Client) removeListener(ch chan struct{}) {
	c.listenersLock.Lock()
	defer c.listenersLock.Unlock()
	for i, l := range c.listeners {
		if l == ch {
			c.listeners = append(c.listeners[:i], c.listeners[i+1:]...)
			break
		}
	}
}

func (c *Client) wakeListeners() {
	c.listenersLock.Lock()
	defer c.listenersLock.Unlock()
	for _, ch := range c.listeners {
		close(ch)
	}
}

func (c *Client) registerSubscribingController(clientID, url string) *registeredController {
	c.registeredControllersLock.Lock()
	defer c.registeredControllersLock.Unlock()

	// Existing controller ... reset its timeout.
	for _, rc := range c.registeredControllers {
		if rc.controller.ClientID() == clientID {
			c.Logger.Debugf("resetting timeout for subscribing controller %s", clientID)
			rc.timeout.Reset(controllerTimeout)
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
	}
	c.registeredControllers = append(c.registeredControllers, rc)
	return rc
}

func (c *Client) forgetController(clientID string) {
	c.registeredControllersLock.Lock()
	defer c.registeredControllersLock.Unlock()
	for i, rc := range c.registeredControllers {
		if rc.controller.ClientID() == clientID {
			c.Logger.Infof("forgetting controller %s", clientID)
			c.registeredControllers = append(c.registeredControllers[:i], c.registeredControllers[i+1:]...)
			rc.timeout.Stop()
			break
		}
	}
}

func (c *Client) notifyControllers() {
	c.registeredControllersLock.Lock()
	defer c.registeredControllersLock.Unlock()
	for _, rc := range c.registeredControllers {
		c.SendTimeline(rc)
	}
}

func (c *Client) SendTimeline(rc *registeredController) error {
	c.Logger.Debugf("sending timeline to %s", rc.controller.String())
	err := rc.controller.Send(
		c.ID,
		&MediaContainer{
			MachineIdentifier: c.ID,
			Timelines:         []Timeline{c.timeline},
		},
	)
	if err != nil {
		c.Logger.Errorf("error sending timeline to controller %s: %s",
			rc.controller.ClientID(), err)
	}
	return err
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
