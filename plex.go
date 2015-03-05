package plexible

// MediaContainer is the top-level struct most Plex communication stanzas.
type MediaContainer struct {
	CommandID         string     `xml:"commandID,attr,omitempty"`
	MachineIdentifier string     `xml:"machineIdentifier,attr,omitempty"`
	Timelines         []Timeline `xml:"Timeline,omitempty"`
	Players           []player   `xml:"Player,omitempty"`
}

// Player capabilities.
const (
	CapabilityTimeline   = "timeline"
	CapabilityPlayback   = "playback"
	CapabilityNavigation = "navigation"
	CapabilityMirror     = "mirror"
	CapabilityPlayQueues = "playqueues"
)

// Timeline repesents the current state of a Player.
type Timeline struct {
	State    string `xml:"state,attr,omitempty"`
	Duration int64  `xml:"duration,attr,omitempty"`
	Time     int64  `xml:"time,attr,omitempty"`
	Type     string `xml:"type,attr,omitempty"`
}

// Player types.
const (
	TypeMusic = "music"
	TypePhoto = "photo"
	TypeVideo = "video"
)

// Timeline states.
const (
	StateStopped   = "stopped"
	StatePaused    = "paused"
	StatePlaying   = "playing"
	StateBuffering = "buffering"
	StateError     = "error"
)

const (
	discoveryIP         = "239.0.0.250"
	clientDiscoveryPort = 32412
	clientBroadcastPort = 32413
	serverDiscoveryPort = 32414
)

type player struct {
	Title                string `xml:"title,attr"`
	MachineIdentifier    string `xml:"machineIdentifier,attr"`
	Product              string `xml:"product,attr"`
	Version              string `xml:"version,attr"`
	ProtocolVersion      string `xml:"protocolVersion,attr"`
	ProtocolCapabilities string `xml:"protocolCapabilities,attr"`
	DeviceClass          string `xml:"deviceClass,attr"`
}
