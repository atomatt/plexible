package main

import (
	"flag"
	"os"
	"os/signal"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/emgee/plexible"
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

	client := plexible.NewClient(
		&plexible.ClientInfo{
			"862b2506-ba0a-11e4-b501-cf0a1568e6a3",
			"sharkbait",
			"GoPlex",
			"0.0.1",
		},
		logger,
	)

	player := NewPlayer(logger)
	client.AddPlayer(
		plexible.TypeMusic,
		[]string{plexible.CapabilityTimeline, plexible.CapabilityPlayback},
		player.timelines,
		player.cmds,
	)
	player.timelines <- &plexible.PlayerTimeline{
		State: plexible.StateStopped,
	}

	if err := client.Start(); err != nil {
		logger.Fatalf("error starting client: %s", err)
	}
	defer client.Stop()

	sigs := make(chan os.Signal)
	signal.Notify(sigs, os.Interrupt)
	<-sigs
}

type Player struct {
	logger    *logrus.Logger
	cmds      chan interface{}
	timelines chan *plexible.PlayerTimeline
}

func NewPlayer(logger *logrus.Logger) *Player {
	p := &Player{
		logger:    logger,
		cmds:      make(chan interface{}),
		timelines: make(chan *plexible.PlayerTimeline),
	}
	go p.cmdLoop()
	return p
}

// run in goroutine
func (p *Player) cmdLoop() {
	p.logger.Info("player loop started")
	defer p.logger.Info("player loop ended")

	var ticker *time.Ticker
	var tickerC <-chan time.Time

	state := plexible.StateStopped
	var containerKey string
	var tracks []plexible.Track
	var playTime uint64 = 0

	for {
		select {
		case <-tickerC:
			if state == plexible.StatePlaying {
				playTime += 1000
			}
		case cmd := <-p.cmds:
			p.logger.Debugf("cmd=%#v", cmd)
			switch v := cmd.(type) {
			case *plexible.PlayMediaCommand:
				// Set initial play state.
				state = plexible.StatePlaying
				containerKey = v.ContainerKey
				tracks = v.MediaContainer.Tracks
				playTime = 0
				// Start ticker for time updates.
				ticker = time.NewTicker(time.Second)
				tickerC = ticker.C
			case *plexible.PauseCommand:
				// Stop ticker.
				tickerC = nil
				ticker.Stop()
				// Update play state
				state = plexible.StatePaused
			case *plexible.PlayCommand:
				// Update play state.
				state = plexible.StatePlaying
				// Start ticker
				ticker = time.NewTicker(time.Second)
				tickerC = ticker.C
			case *plexible.StopCommand:
				// Stop ticker.
				tickerC = nil
				ticker.Stop()
				// Clear play state.
				state = plexible.StateStopped
				containerKey = ""
				tracks = nil
				playTime = 0
			}
		}
		t := &plexible.PlayerTimeline{State: state}
		if tracks != nil {
			t.Time = playTime
			t.ContainerKey = containerKey
			t.RatingKey = tracks[0].RatingKey
			t.Key = tracks[0].Key
			t.Duration = tracks[0].Duration
		}
		p.timelines <- t
	}
}
