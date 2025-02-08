package bhapticsgo

import (
	"sync"

	"github.com/enriquebris/goconcurrentqueue"
	"github.com/gorilla/websocket"
)

const (
	endpoint       = "v2/feedbacks%s"
	bHapticsApiUrl = "ws://%s:%d/%s"
)

// BHaptics Positions
const (
	VestPosition      bHapticsPosition = "Vest"
	VestFrontPosition bHapticsPosition = "VestFront"
	VestBackPosition  bHapticsPosition = "VestBack"
	ForearmLPosition  bHapticsPosition = "ForearmL"
	ForearmRPosition  bHapticsPosition = "ForearmR"
)

// BHaptics Status
const (
	Disconnected bHapticsStatus = 0
	Connecting   bHapticsStatus = 1
	Connected    bHapticsStatus = 2
)

type bHapticsPosition string
type bHapticsStatus int

type BHapticsManager struct {
	sync.Mutex

	IsConnected bool

	connection    *bHapticsConnection
	eventCache    map[string][]event
	eventQueue    *goconcurrentqueue.FIFO // 요청 큐
	registerQueue *goconcurrentqueue.FIFO
	appKey        string
	appName       string
	debugMode     bool
}

type bHapticsConnection struct {
	ipAddress string
	port      int

	socket       *websocket.Conn
	timeout      int
	write        chan []byte
	read         chan []byte
	lastResponse playerResponse
}

type Option struct {
	// BHaptics App Key
	AppKey string
	// BHaptics App Name
	AppName string
	// BHaptics Remote IP Address
	IPAddress string
	// BHaptics Remote Port
	Port int
	// BHaptics Connection Timeout (in seconds)
	Timeout int
	// Debug Mode
	DebugMode bool
}

type playerResponse struct {
	isReady bool

	ConnectedDeviceCount int                    `json:"connectedDeviceCount,omitempty"`
	ActiveKeys           []string               `json:"activeKeys,omitempty"`
	ConnectedPositions   []bHapticsPosition     `json:"connectedPositions,omitempty"`
	RegisteredKeys       []string               `json:"registeredKeys,omitempty"`
	Status               map[string]interface{} `json:"status,omitempty"`
}

type eventRequest struct {
	Submit []event `json:"submit"`
}

type event struct {
	Key    string       `json:"key"`
	Type   string       `json:"type"`
	Frame  framePayload `json:"frame"`
	AltKey string       `json:"altKey,omitempty"`
}

type framePayload struct {
	Position       bHapticsPosition `json:"position"`
	DotPoints      []HapticPoint    `json:"dotPoints,omitempty"`
	PathPoints     []HapticPoint    `json:"pathPoints,omitempty"`
	DurationMillis int              `json:"durationMillis"`
}

type registerRequest struct {
	Register []register `json:"Register"`
}

type register struct {
	Key     string  `json:"Key"`
	Project project `json:"Project"`
}

type project struct {
	Layout interface{} `json:"layout"`
	Tracks interface{} `json:"tracks"`
}

type HapticPoint struct {
	Index     int         `json:"index,omitempty"`
	X         float64     `json:"x,omitempty"`
	Y         float64     `json:"y,omitempty"`
	Intensity interface{} `json:"intensity,omitempty"`
}
