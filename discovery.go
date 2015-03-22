package plexible

import (
	"bytes"
	"fmt"
	"net"
	"strconv"

	"github.com/Sirupsen/logrus"
)

var (
	// StandardClientDiscoveryAddr is the standard UDP broadcast address used
	// for Plex client discovery.
	StandardClientDiscoveryAddr = net.UDPAddr{
		IP:   net.ParseIP(discoveryIP),
		Port: clientDiscoveryPort,
	}
	// StandardClientBroadcastAddr is the standard UDP broadcast address used
	// for Plex client announcements.
	StandardClientBroadcastAddr = net.UDPAddr{
		IP:   net.ParseIP(discoveryIP),
		Port: clientBroadcastPort,
	}
)

// ClientDiscovery handles local network discovery on behalf of a client.
//
// The client should annouce its arrival and departure by calling Hello() and Bye(). It should also start a
type ClientDiscovery struct {
	Info   *ClientInfo
	Port   int
	Logger *logrus.Logger
}

// ListenAndServe creates a UDP connection to listen for discovery requests and
// calls Serve(). If addr is nil, StandardClientDiscoveryAddr is used.
func (d *ClientDiscovery) ListenAndServe(addr *net.UDPAddr) error {
	if addr == nil {
		addr = &StandardClientDiscoveryAddr
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("error creating client discovery socket (%s)", err)
	}
	return d.Serve(conn)
}

// Serve loops forever to handle discovery requests on the UDP connection.
func (d *ClientDiscovery) Serve(conn *net.UDPConn) error {
	defer conn.Close()
	for {
		b := make([]byte, 1024)
		_, addr, err := conn.ReadFrom(b)
		if err != nil {
			return err
		}
		msg := message("HTTP/1.0 200 OK", d.Info, d.Port)
		d.Logger.Debugf("client discovery request from %s", addr)
		d.Logger.Debugf("sending client discovery response: %q", msg)
		_, err = conn.WriteTo(msg, addr)
		if err != nil {
			return err
		}
	}
}

// Hello announces the client's arrival to the Plex network over UDP. If addr
// is nil, StandardClientBroadcastAddr is used.
func (d *ClientDiscovery) Hello(addr *net.UDPAddr) error {
	d.Logger.Info("announcing client to network")
	msg := message("HELLO * HTTP/1.0", d.Info, d.Port)
	d.Logger.Debugf("sending %q", msg)
	return send(msg, addr)
}

// Bye announces the client's departure to the Plex network over UDP. If addr
// is nil, StandardClientBroadcastAddr is used.
func (d *ClientDiscovery) Bye(addr *net.UDPAddr) error {
	d.Logger.Info("removing client from network")
	msg := message("BYE * HTTP/1.0", d.Info, d.Port)
	d.Logger.Debugf("sending %q", msg)
	return send(msg, addr)
}

func send(msg []byte, addr *net.UDPAddr) error {

	if addr == nil {
		addr = &StandardClientBroadcastAddr
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return fmt.Errorf("error dialing %s (%s)", addr, err)
	}
	defer conn.Close()

	_, err = conn.Write(msg)
	if err != nil {
		return fmt.Errorf("error writing msg (%s)", addr, err)
	}

	return nil
}

func message(header string, info *ClientInfo, port int) []byte {

	params := map[string]string{
		"Content-Type":     "plex/media-player",
		"Name":             info.Name,
		"Port":             strconv.Itoa(port),
		"Product":          info.Product,
		"Protocol":         "plex",
		"Protocol-Version": "1",
		// This should come from the client, but I suspect it's irrelevant as
		// it's the client's players that really have capabilities and those
		// capabilities are returned by the API.
		//"Protocol-Capabilities": "timeline,playback",
		"Resource-Identifier": info.ID,
		"Version":             info.Version,
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
