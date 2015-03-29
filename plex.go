package plexible

// MediaContainer is the top-level struct most Plex communication stanzas.
type MediaContainer struct {
	CommandID         string     `xml:"commandID,attr,omitempty"`
	MachineIdentifier string     `xml:"machineIdentifier,attr,omitempty"`
	Timelines         []Timeline `xml:"Timeline,omitempty"`
	Players           []player   `xml:"Player,omitempty"`
	Tracks            []Track    `xml:"Track,omitempty"`
}

// Track is an audio track in a MediaContainer.
type Track struct {
	PlayQueueItemID      int    `xml:"playQueueItemID,attr,omitempty"`
	RatingKey            int    `xml:"ratingKey,attr,omitempty"`
	Key                  string `xml:"key,attr,omitempty"`
	ParentRatingKey      int    `xml:"parentRatingKey,attr,omitempty"`
	GrandparentRatingKey int    `xml:"grandparentRatingKey,attr,omitempty"`
	GUID                 string `xml:"guid,attr,omitempty"`
	Type                 string `xml:"type_,attr,omitempty"`
	Title                string `xml:"title,attr,omitempty"`
	TitleSort            string `xml:"titleSort,attr,omitempty"`
	GrandparentKey       string `xml:"grandparentKey,attr,omitempty"`
	ParentKey            string `xml:"parentKey,attr,omitempty"`
	GrandparentTitle     string `xml:"grandparentTitle,attr,omitempty"`
	ParentTitle          string `xml:"parentTitle,attr,omitempty"`
	OriginalTitle        string `xml:"originalTitle,attr,omitempty"`
	Summary              string `xml:"summary,attr,omitempty"`
	Index                int    `xml:"index,attr,omitempty"`
	ParentIndex          int    `xml:"parentIndex,attr,omitempty"`
	ViewCount            int    `xml:"viewCount,attr,omitempty"`
	LastViewedAt         int    `xml:"lastViewedAt,attr,omitempty"`
	Thumb                string `xml:"thumb,attr,omitempty"`
	ParentThumb          string `xml:"parentThumb,attr,omitempty"`
	GrandparentThumb     string `xml:"grandparentThumb,attr,omitempty"`
	Duration             int    `xml:"duration,attr,omitempty"`
	AddedAt              int    `xml:"addedAt,attr,omitempty"`
	UpdatedAt            int    `xml:"updatedAt,attr,omitempty"`
	Media                *Media `xml:"Media,omitempty"`
}

// Media is an audio track media element.
type Media struct {
	ID            int    `xml:"id,attr,omitempty"`
	Duration      int    `xml:"duration,attr,omitempty"`
	Bitrate       int    `xml:"bitrate,attr,omitempty"`
	AudioChannels int    `xml:"audioChannels,attr,omitempty"`
	AudioCodec    string `xml:"audioCodec,attr,omitempty"`
	Container     string `xml:"container,attr,omitempty"`
	Part          *Part  `xml:"Part,omitempty"`
}

// Part is an audo track media part.
type Part struct {
	ID        int      `xml:"id,attr,omitempty"`
	Key       string   `xml:"key,attr,omitempty"`
	Duration  int      `xml:"duration,attr,omitempty"`
	File      string   `xml:"file,attr,omitempty"`
	Size      int      `xml:"size,attr,omitempty"`
	Container string   `xml:"container,attr,omitempty"`
	Streams   []Stream `xml:"Stream,omitempty"`
}

// Stream is an audio track media stream.
type Stream struct {
	ID           int    `xml:"id,attr,omitempty"`
	StreamType   int    `xml:"streamType,attr,omitempty"`
	Selected     int    `xml:"selected,attr,omitempty"`
	Codec        string `xml:"codec,attr,omitempty"`
	Index        int    `xml:"index,attr,omitempty"`
	Channels     int    `xml:"channels,attr,omitempty"`
	Bitrate      int    `xml:"bitrate,attr,omitempty"`
	BitrateMode  string `xml:"bitrateMode,attr,omitempty"`
	Duration     int    `xml:"duration,attr,omitempty"`
	SamplingRate int    `xml:"samplingRate,attr,omitempty"`
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

// PlayMediaCommand is sent to a player to start playback of new media.
type PlayMediaCommand struct {
	ServerURL      string
	MediaContainer *MediaContainer
}

// PauseCommand is sent to a player to pause playback.
type PauseCommand struct {
}

// PlayCommand is sent to a player to resume playback.
type PlayCommand struct {
}

// StopCommand is sent to a player to stop playback.
type StopCommand struct {
}
