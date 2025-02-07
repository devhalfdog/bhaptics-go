package bhapticsgo

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

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
	registerCache []int                   // TODO - []Register
	eventQueue    *goconcurrentqueue.FIFO // 요청 큐
	registerQueue *goconcurrentqueue.FIFO
	appKey        string
	appName       string
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
}

type playerResponse struct {
	isReady bool

	ConnectedDeviceCount int                    `json:"connectedDeviceCount,omitempty"`
	ActiveKeys           []string               `json:"activeKeys,omitempty"`
	ConnectedPositions   []bHapticsPosition     `json:"connectedPositions,omitempty"`
	RegisteredKeys       []string               `json:"registeredKeys,omitempty"`
	Status               map[string]interface{} `json:"status,omitempty"`
}

func NewBHapticsManager(opt ...Option) *BHapticsManager {
	manager := &BHapticsManager{
		connection: &bHapticsConnection{
			ipAddress: "localhost",
			port:      15881,
			socket:    nil,
			timeout:   3,
			write:     make(chan []byte, 1024),
			read:      make(chan []byte, 1024),
		},
		eventQueue:  goconcurrentqueue.NewFIFO(),
		IsConnected: false,
		appKey:      "",
		appName:     "",
	}

	if len(opt) > 0 {
		manager.connection.ipAddress = opt[0].IPAddress
		manager.connection.port = opt[0].Port
		manager.connection.timeout = opt[0].Timeout

		manager.appKey = opt[0].AppKey
		manager.appName = opt[0].AppName
	}

	return manager
}

func (m *BHapticsManager) Run() error {
	m.Lock()
	defer m.Unlock()

	err := m.connect()
	if err != nil {
		log.Println(err)
		return err
	}

	go m.reader()
	go m.parser()
	go m.writer()

	return nil
}

func (m *BHapticsManager) connect() error {
	if m.IsConnected || m.connection.socket != nil {
		return fmt.Errorf("already connected")
	}

	var ep string
	if m.appKey != "" && m.appName != "" {
		ep = fmt.Sprintf(endpoint, fmt.Sprintf("?appKey=%s&appName=%s", m.appKey, m.appName))
	}

	apiUrl := fmt.Sprintf(bHapticsApiUrl, m.connection.ipAddress, m.connection.port, ep)

	dialer := websocket.Dialer{
		HandshakeTimeout: time.Duration(m.connection.timeout) * time.Second,
		NetDial: func(network, addr string) (net.Conn, error) {
			conn, err := net.Dial(network, addr)
			if err != nil {
				log.Printf("Failed to connect to %s: %v", addr, err)
				return nil, err
			}

			if tcpConn, ok := conn.(*net.TCPConn); ok {
				tcpConn.SetNoDelay(true)
			}

			return conn, nil
		},
	}

	var err error
	m.connection.socket, _, err = dialer.Dial(apiUrl, nil)
	if err != nil {
		log.Printf("Failed to connect to BHaptics API: %v", err)
		m.connection.socket = nil
		return err
	}

	m.IsConnected = true

	return nil
}

func (m *BHapticsManager) disconnect() error {
	m.Lock()
	defer m.Unlock()

	if m.connection.socket == nil {
		return nil
	}

	defer func() {
		m.connection.socket.Close()
		m.connection.socket = nil
		m.connection.lastResponse = playerResponse{}
		m.IsConnected = false
	}()

	err := m.connection.socket.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseMessage, ""))
	if err != nil {
		log.Printf("Failed to close WebSocket connection: %v", err)
		return err
	}

	return nil
}

func (m *BHapticsManager) reader() {
	// defer가 실행되면 for문이 끝난건데 그것은 에러
	defer func() {
		log.Println("reader goroutine exited")
		m.disconnect()
	}()

	for {
		if m.connection.socket == nil || !m.IsConnected {
			log.Println("websocket is not connected")
			return
		}

		_, message, err := m.connection.socket.ReadMessage()
		if err != nil {
			m.connection.read <- message
			continue
		}

		m.connection.read <- message
	}
}

func (m *BHapticsManager) parser() {
	defer func() {
		log.Println("parser goroutine exited")
		m.disconnect()
	}()

	for message := range m.connection.read {
		if strings.Split(string(message), " ")[0] == "error" {
			log.Printf("error message from BHaptics API: %s", string(message))
			continue
		}

		var response playerResponse
		err := json.Unmarshal(message, &response)
		if err != nil {
			log.Printf("failed to parse JSON: %v", err)
			continue
		}

		response.isReady = true

		m.connection.lastResponse = response
	}
}

func (m *BHapticsManager) writer() {
	defer func() {
		log.Println("writer goroutine exited")
		m.disconnect()
	}()

	for message := range m.connection.write {
		err := m.connection.socket.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			log.Printf("failed to send message to BHaptics API: %v", err)
			continue
		}
	}
}

func (m *BHapticsManager) GetConnectedDeviceCount() int {
	m.Lock()
	defer m.Unlock()

	return m.connection.lastResponse.ConnectedDeviceCount
}

func (m *BHapticsManager) IsDeviceConnected(position bHapticsPosition) bool {
	m.Lock()
	defer m.Unlock()

	if position == VestFrontPosition || position == VestBackPosition {
		position = VestPosition
	}

	for _, p := range m.connection.lastResponse.ConnectedPositions {
		if p == position {
			return true
		}
	}

	return false
}

// TODO
func (m *BHapticsManager) GetDeviceStatus(position bHapticsPosition) []int {
	m.Lock()
	defer m.Unlock()

	if m.connection == nil || !m.connection.lastResponse.isReady {
		log.Printf("websocket is not connected or response is not ready")
		return nil
	}

	statusMap := m.connection.lastResponse.Status
	if statusMap == nil {
		log.Printf("status map is nil")
		return nil
	}

	// TODO
	if position == VestPosition {
		return nil
	}

	return nil
}

func (m *BHapticsManager) IsPlaying(key string) (bool, error) {
	m.Lock()
	defer m.Unlock()

	if !m.connection.lastResponse.isReady {
		log.Println("[IsPlaying] websocket is not connected or response is not ready")
		return false, errors.New("websocket is not connected or response is not ready")
	}

	for _, activeKey := range m.connection.lastResponse.ActiveKeys {
		if activeKey == key {
			return true, nil
		}
	}

	return false, errors.New("key not found")
}

func (m *BHapticsManager) IsPlayingAny() (bool, error) {
	m.Lock()
	defer m.Unlock()

	if !m.connection.lastResponse.isReady {
		log.Println("[IsPlayingAny] websocket is not connected or response is not ready")
		return false, errors.New("websocket is not connected or response is not ready")
	}

	return len(m.connection.lastResponse.ActiveKeys) > 0, nil
}
