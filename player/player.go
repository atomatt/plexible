package main

import (
	"flag"
	"os"
	"os/signal"

	"github.com/Sirupsen/logrus"
	"github.com/emgee/goplex"
)

func main() {

	// Parse flags.
	logLevelFlag := flag.String("log-level", "info", "log level (debug|info|warn|error|fatal|panic)")
	flag.Parse()

	// Parse the log level.
	logLevel, err := logrus.ParseLevel(*logLevelFlag)
	if err != nil {
		logrus.StandardLogger().Fatalf("Invalid log level: %s", *logLevelFlag)
	}

	// Create & configure a logger for the client and player to use.
	logger := logrus.New()
	logger.Level = logLevel

	client := plex.NewClient(
		"862b2506-ba0a-11e4-b501-cf0a1568e6a3",
		"sharkbait",
		"GoPlex",
		"0.0.1",
		logger,
	)

	player := NewPlayer(logger)
	client.AddPlayer(player)

	if err := client.Start(); err != nil {
		logger.Fatalf("error starting client: %s", err)
	}
	defer client.Stop()

	sigs := make(chan os.Signal)
	signal.Notify(sigs, os.Interrupt)
	<-sigs
}

type Player struct {
	logger   *logrus.Logger
	cmds     chan plex.PlayerCommand
	timeline plex.Timeline
	ch       chan plex.Timeline
}

func NewPlayer(logger *logrus.Logger) *Player {
	p := &Player{
		logger: logger,
		cmds:   make(chan plex.PlayerCommand),
		timeline: plex.Timeline{
			Type:  plex.TypeMusic,
			State: plex.StateStopped,
		},
	}
	go p.cmdLoop()
	return p
}

func (p *Player) Capabilities() []string {
	return []string{plex.CapabilityTimeline, plex.CapabilityPlayback}
}

func (p *Player) CommandChan() chan plex.PlayerCommand {
	return p.cmds
}

func (p *Player) Subscribe() chan plex.Timeline {
	p.ch = make(chan plex.Timeline, 1)
	p.ch <- p.timeline
	return p.ch
}

// run in goroutine
func (p *Player) cmdLoop() {
	p.logger.Info("player loop started")
	defer p.logger.Info("player loop ended")
	for {
		cmd := <-p.cmds
		p.logger.Debugf("Cmd: %v, Params: %v", cmd.Type, cmd.Params)
		switch cmd.Type {
		case "playMedia":
			p.timeline.State = plex.StatePlaying
		case "pause":
			p.timeline.State = plex.StatePaused
		case "play":
			p.timeline.State = plex.StatePlaying
		case "stop":
			p.timeline.State = plex.StateStopped
		}
		p.ch <- p.timeline
	}
}
