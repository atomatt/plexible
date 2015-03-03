package plex

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strings"
	"time"
)

type Server struct {
	Addr   net.Addr
	Params map[string]string
}

func DiscoverServers(duration time.Duration) ([]*Server, error) {

	// Create UDP socket with OS-assigned port.
	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Broadcast discovery message to Plex server port.
	conn.WriteTo(
		[]byte("M-SEARCH * HTTP/1.0"),
		&net.UDPAddr{IP: net.ParseIP(discoveryIP), Port: serverDiscoveryPort},
	)

	// Start goroutine to listen for server responses.
	ch := make(chan *Server)
	go func() {
		b := make([]byte, 1024)
		n, addr, err := conn.ReadFrom(b)
		if err != nil {
			return
		}
		params, err := parseServerResponse(b[:n])
		if err != nil {
			return
		}
		ch <- &Server{addr, params}
	}()

	// Collect servers until the timeout.
	servers := []*Server{}
	timeout := time.After(duration)
Collection:
	for {
		select {
		case s := <-ch:
			servers = append(servers, s)
		case <-timeout:
			break Collection
		}
	}

	return servers, nil
}

func parseServerResponse(b []byte) (map[string]string, error) {
	params := map[string]string{}
	s := bufio.NewScanner(bytes.NewReader(b))
	first := true
	for s.Scan() {
		line := s.Text()
		if first {
			if line != "HTTP/1.0 200 OK" {
				return nil, fmt.Errorf("Unrecognised response header: %s", line)
			}
			first = false
			continue
		}
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		params[parts[0]] = strings.TrimSpace(parts[1])
	}
	return params, nil
}
