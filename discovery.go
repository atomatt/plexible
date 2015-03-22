package plexible

import (
	"bytes"
	"fmt"
	"net"
	"strconv"

	"github.com/Sirupsen/logrus"
)

// ClientDiscovery handles local network discovery on behalf of a client.
type ClientDiscovery struct {
	Info   *ClientInfo
	Port   int
	Logger *logrus.Logger

	conn    *net.UDPConn
	stopped chan struct{}
}

// NewClientDiscovery allocates and returns a new ClientDiscovery.
func NewClientDiscovery(info *ClientInfo, port int, logger *logrus.Logger) *ClientDiscovery {
	return &ClientDiscovery{Info: info, Port: port, Logger: logger}
}

// Start announces the client on the network and listens for broadcast requests
// from other devices that are interested in clients.
func (d *ClientDiscovery) Start() error {

	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.ParseIP(discoveryIP),
		Port: clientDiscoveryPort,
	})
	if err != nil {
		return fmt.Errorf("error creating client discovery socket (%s)", err)
	}
	d.conn = conn

	err = d.hello()
	if err != nil {
		return fmt.Errorf("error saying hello (%s)", err)
	}

	d.Logger.Infof("listening for client discovery requests on port %d", clientDiscoveryPort)
	d.stopped = make(chan struct{})

	go func() {
		d.Logger.Info("client discovery loop running")
		defer func() {
			d.Logger.Info("client discovery loop ending")
			close(d.stopped)
		}()
		for {
			b := make([]byte, 1024)
			_, addr, err := d.conn.ReadFrom(b)
			if err != nil {
				return
			}

			msg := message("HTTP/1.0 200 OK", d)
			d.Logger.Debugf("client discovery request from %s", addr)
			d.Logger.Debugf("sending client discovery response: %q", msg)
			d.conn.WriteTo(msg, addr)
		}
	}()

	return nil
}

// Stop shutdowns the broadcast listener and announces the client's removal
// from the network.
func (d *ClientDiscovery) Stop() error {
	d.bye()
	d.conn.Close()
	<-d.stopped
	return nil
}

func (d *ClientDiscovery) hello() error {
	d.Logger.Info("announcing client to network")
	msg := message("HELLO * HTTP/1.0", d)
	d.Logger.Debugf("HELLO: %q", msg)
	_, err := d.conn.WriteTo(msg, &net.UDPAddr{
		IP:   net.ParseIP(discoveryIP),
		Port: clientBroadcastPort,
	})
	if err != nil {
		return fmt.Errorf("error sending HELLO (%s)", err)
	}
	return nil
}

func (d *ClientDiscovery) bye() error {
	d.Logger.Info("removing player from network")
	msg := message("BYE * HTTP/1.0", d)
	d.Logger.Debugf("BYE: %q", msg)
	_, err := d.conn.WriteTo(msg, &net.UDPAddr{
		IP:   net.ParseIP(discoveryIP),
		Port: clientBroadcastPort,
	})
	if err != nil {
		return fmt.Errorf("error sending BYE (%s)", err)
	}
	return nil
}

func message(header string, d *ClientDiscovery) []byte {

	params := map[string]string{
		"Content-Type":     "plex/media-player",
		"Name":             d.Info.Name,
		"Port":             strconv.Itoa(d.Port),
		"Product":          d.Info.Product,
		"Protocol":         "plex",
		"Protocol-Version": "1",
		// This should come from the client, but I suspect it's irrelevant as
		// it's the client's players that really have capabilities and those
		// capabilities are returned by the API.
		//"Protocol-Capabilities": "timeline,playback",
		"Resource-Identifier": d.Info.ID,
		"Version":             d.Info.Version,
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
