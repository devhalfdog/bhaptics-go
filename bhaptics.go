/*
   이 라이브러리는 BHaptics API를 Go로 구현한 것입니다.
   참고한 예제
   : https://github.com/HerpDerpinstine/bHapticsLib
   : https://github.com/bHaptics
*/

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

type request struct {
	Submit []eventRequest `json:"submit"`
}

type eventRequest struct {
	Key   string       `json:"key"`
	Type  string       `json:"type"`
	Frame framePayload `json:"frame"`
}

type framePayload struct {
	Position       bHapticsPosition `json:"position"`
	DotPoints      []HapticPoint    `json:"dotPoints,omitempty"`
	PathPoints     []HapticPoint    `json:"pathPoints,omitempty"`
	DurationMillis int              `json:"durationMillis"`
}

type HapticPoint struct {
	Index     int         `json:"index,omitempty"`
	X         float64     `json:"x,omitempty"`
	Y         float64     `json:"y,omitempty"`
	Intensity interface{} `json:"intensity,omitempty"`
}

// NewBHapticsManager 함수는 새로운 BHapticsManager 인스턴스를 반환한다
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
		debugMode:   true,
	}

	if len(opt) > 0 {
		if opt[0].IPAddress != "" {
			manager.connection.ipAddress = opt[0].IPAddress
		}

		if opt[0].Port > 0 {
			manager.connection.port = opt[0].Port
		}

		if opt[0].Timeout > 0 {
			manager.connection.timeout = opt[0].Timeout
		}

		manager.appKey = opt[0].AppKey
		manager.appName = opt[0].AppName
		manager.debugMode = opt[0].DebugMode
	}

	return manager
}

// Run 메서드는 bHaptics API와 연결을 시작하고
// 데이터를 처리하기 위한 goroutine을 시작한다
// 연결에 실패할 경우 에러를 반환한다
func (m *BHapticsManager) Run() error {
	m.Lock()
	defer m.Unlock()

	err := m.connect()
	if err != nil {
		m.debug("[Run] ", err)
		return err
	}

	go m.reader()
	go m.parser()
	go m.writer()
	go m.eventSend()

	return nil
}

// connect 메서드는 bHaptics API와 WebSocket 연결을 시도한다
// 연결에 실패할 경우 에러를 반환한다
func (m *BHapticsManager) connect() error {
	if m.IsConnected || m.connection.socket != nil {
		m.debug("[connect] Already connected")
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
				m.debug("[connect.dialer] Failed to connect to ", addr, ": ", err)
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
		m.debug("[connect] Failed to connect to BHaptics API: ", err)
		m.connection.socket = nil
		return err
	}

	m.IsConnected = true

	return nil
}

// disconnect 메서드는 bHaptics API와 연결된
// WebSocket 연결을 종료한다
// 연결 종료에 실패할 경우 에러를 반환한다
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
		m.debug("[disconnect] Failed to close WebSocket connection: ", err)
	}

	return err
}

// reader 메서드는 bHaptics API로부터 WebSocket로 받은
// 데이터를 읽기 채널에 전달한다
func (m *BHapticsManager) reader() {
	// defer가 실행되면 for문이 종료된거라 그것은 에러
	defer func() {
		m.debug("[reader] reader goroutine exited")
		m.disconnect()
	}()

	for {
		if m.connection.socket == nil || !m.IsConnected {
			m.debug("[reader] websocket is not connected")
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

// parser 메서드는 읽기 채널에 데이터가 있을 때
// 해당하는 데이터를 구조체로 변경한다
func (m *BHapticsManager) parser() {
	defer func() {
		m.debug("[parser] parser goroutine exited")
		m.disconnect()
	}()

	for message := range m.connection.read {
		//! TODO: error handling
		if strings.Split(string(message), " ")[0] == "error" {
			m.debug("[parser] error message from BHaptics API: ", string(message))
			continue
		}

		var response playerResponse
		err := json.Unmarshal(message, &response)
		if err != nil {
			m.debug("[parser] failed to parse JSON: ", err)
			continue
		}

		response.isReady = true

		m.connection.lastResponse = response
	}
}

// writer 메서드는 쓰기 채널에 데이터가 있을 때
// 데이터를 WebSocket으로 전송한다
func (m *BHapticsManager) writer() {
	defer func() {
		m.debug("[writer] writer goroutine exited")
		m.disconnect()
	}()

	for message := range m.connection.write {
		err := m.connection.socket.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			m.debug("[writer] failed to send message to BHaptics API: ", err)
			continue
		}
	}
}

// eventAdd 메서드는 Queue에 이벤트를 추가한다
func (m *BHapticsManager) eventAdd(event []eventRequest) error {
	m.Lock()
	defer m.Unlock()

	if m.connection.socket == nil || !m.IsConnected {
		m.debug("[eventAdd] websocket is not connected")
		return fmt.Errorf("websocket is not connected")
	}

	req := request{
		Submit: event,
	}

	msg, err := json.Marshal(req)
	if err != nil {
		m.debug("[send] failed to marshal JSON: ", err)
		return err
	}

	err = m.eventQueue.Enqueue(msg)
	if err != nil {
		m.debug("[send] failed to enqueue event request: ", err)
		return err
	}

	return nil
}

// eventSend 메서드는 계속 대기하면서 Queue에 이벤트가 있을 때
// 쓰기 채널에 이벤트 데이터를 전달한다
func (m *BHapticsManager) eventSend() error {
	defer func() {
		m.debug("[send] eventSend goroutine exited")
		m.disconnect()
	}()

	for {
		if m.connection.socket == nil || !m.IsConnected {
			m.debug("[send] websocket is not connected")
			return fmt.Errorf("websocket is not connected")
		}

		playing, err := m.IsPlayingAny()
		// 플레이 중이지 않으며 에러가 없을 때
		// Queue에 이벤트가 있으면 쓰기 채널에 이벤트를 전달
		if !playing && err == nil {
			if m.eventQueue.GetLen() > 0 {
				// Queue에 있는 이벤트를 가져온다
				m.debug("[send] event dequeued")
				item, err := m.eventQueue.Dequeue()
				if err != nil {
					m.debug("[send] failed to dequeue event request: ", err)
				} else {
					m.debug("[send] send event")
					m.connection.write <- item.([]byte)
				}
			}
		} else {
			m.debug("[send] not sending event, playing: ", playing, ", error: ", err)
		}

		time.Sleep(500 * time.Millisecond)
	}
}

// GetConnectedDeviceCount 메서드는
// bHaptics API로부터 받은 connectedDeviceCount를 반환한다
func (m *BHapticsManager) GetConnectedDeviceCount() (int, error) {
	m.Lock()
	defer m.Unlock()

	if m.connection == nil || !m.IsConnected {
		m.debug("[GetConnectedDeviceCount] websocket is not connected")
		return -1, errors.New("websocket is not connected")
	}

	if !m.connection.lastResponse.isReady {
		m.debug("[GetConnectedDeviceCount] response is not ready")
		return -1, errors.New("response is not ready")
	}

	return m.connection.lastResponse.ConnectedDeviceCount, nil
}

// IsDeviceConnected 메서드는 bHaptics API로부터
// position에 해당하는 장치가 연결되어 있는지 여부를 반환한다
func (m *BHapticsManager) IsDeviceConnected(position bHapticsPosition) (bool, error) {
	m.Lock()
	defer m.Unlock()

	if m.connection == nil || !m.IsConnected {
		m.debug("[IsDeviceConnected] websocket is not connected")
		return false, errors.New("websocket is not connected")
	}

	if !m.connection.lastResponse.isReady {
		m.debug("[IsDeviceConnected] response is not ready")
		return false, errors.New("response is not ready")
	}

	if position == VestFrontPosition || position == VestBackPosition {
		position = VestPosition
	}

	for _, p := range m.connection.lastResponse.ConnectedPositions {
		if p == position {
			return true, nil
		}
	}

	return false, nil
}

// GetDeviceStatus 메서드는 position에 해당하는 장치의
// 각 모터에 대한 현재 intensity 값을
// 포함한 정수 배열을 가져온다
func (m *BHapticsManager) GetDeviceStatus(position bHapticsPosition) ([]int, error) {
	m.Lock()
	defer m.Unlock()

	if m.connection == nil || !m.connection.lastResponse.isReady {
		m.debug("[GetDeviceStatus] websocket is not connected or response is not ready")
		return nil, errors.New("websocket is not connected or response is not ready")
	}

	statusMap := m.connection.lastResponse.Status
	if statusMap == nil {
		m.debug("[GetDeviceStatus] status map is nil")
		return nil, errors.New("status map is nil")
	}

	if position == VestPosition {
		frontStatusRaw, frontExists := statusMap[string(VestFrontPosition)]
		backStatusRaw, backExists := statusMap[string(VestBackPosition)]

		if !frontExists || !backExists {
			m.debug("[GetDeviceStatus] front or back status not found")
			return nil, errors.New("front or back status not found")
		}
		m.debug("[GetDeviceStatus] frontStatusRaw Value: ", frontStatusRaw)
		m.debug("[GetDeviceStatus] backStatusRaw Value: ", backStatusRaw)

		frontStatus, ok := frontStatusRaw.([]interface{})
		if !ok {
			m.debug("[GetDeviceStatus] failed to convert front status to []interface{}")
			return nil, errors.New("failed to convert front status to []interface{}")
		}

		backStatus, ok := backStatusRaw.([]interface{})
		if !ok {
			m.debug("[GetDeviceStatus] failed to convert back status to []interface{}")
			return nil, errors.New("failed to convert back status to []interface{}")
		}

		totalCount := len(frontStatus) + len(backStatus)
		val := make([]int, totalCount)
		for i := 0; i < totalCount; i++ {
			if i < len(frontStatus) {
				v, ok := frontStatus[i].(float64)
				if !ok {
					m.debug("[GetDeviceStatus] failed to convert front status value to int")
					return nil, errors.New("failed to convert front status value to int")
				}
				val[i] = int(v)
			} else {
				v, ok := backStatus[i-len(frontStatus)].(float64)
				if !ok {
					m.debug("[GetDeviceStatus] failed to convert back status value to int")
					return nil, errors.New("failed to convert back status value to int")
				}
				val[i] = int(v)
			}
		}

		return val, nil
	}

	statusRaw, exists := statusMap[string(position)]
	if !exists {
		m.debug("[GetDeviceStatus] device status not found")
		return nil, errors.New("device status not found")
	}
	m.debug("[GetDeviceStatus] statusRaw Value: ", statusRaw)

	status, ok := statusRaw.([]interface{})
	if !ok {
		m.debug("[GetDeviceStatus] failed to convert device status to []interface{}")
		return nil, errors.New("failed to convert device status to []interface{}")
	}

	val := make([]int, len(status))
	for i := 0; i < len(status); i++ {
		v, ok := status[i].(float64)
		if !ok {
			m.debug("[GetDeviceStatus] failed to convert device status value to int")
			return nil, errors.New("failed to convert device status value to int")
		}
		val[i] = int(v)
	}

	return val, nil
}

// IsPlaying 메서드는 key에 해당하는 이벤트가
// 이미 실행중인지 여부를 반환한다
func (m *BHapticsManager) IsPlaying(key string) (bool, error) {
	m.Lock()
	defer m.Unlock()

	if m.connection == nil || !m.IsConnected {
		m.debug("[IsPlaying] websocket is not connected")
		return false, errors.New("websocket is not connected")
	}

	if !m.connection.lastResponse.isReady {
		m.debug("[IsPlaying] response is not ready")
		return false, errors.New("response is not ready")
	}

	for _, activeKey := range m.connection.lastResponse.ActiveKeys {
		if activeKey == key {
			return true, nil
		}
	}

	m.debug("[IsPlaying] key not found")
	return false, errors.New("key not found")
}

// IsPlayingAny 메서드는 이벤트가 이미 실행중인지 여부를 반환한다
func (m *BHapticsManager) IsPlayingAny() (bool, error) {
	m.Lock()
	defer m.Unlock()

	if m.connection == nil || !m.IsConnected {
		m.debug("[IsPlayingAny] websocket is not connected")
		return false, errors.New("websocket is not connected")
	}

	if !m.connection.lastResponse.isReady {
		m.debug("[IsPlayingAny] response is not ready")
		return false, errors.New("response is not ready")
	}

	return len(m.connection.lastResponse.ActiveKeys) > 0, nil
}

// StopPlaying 메서드는 key에 해당하는 이벤트를 중지시킨다
func (m *BHapticsManager) StopPlaying(key string) error {
	m.Lock()
	defer m.Unlock()

	if m.connection == nil || !m.IsConnected {
		m.debug("[StopPlaying] websocket is not connected")
		return errors.New("websocket is not connected")
	}

	events := []eventRequest{
		{
			Key:  key,
			Type: "turnOff",
		},
	}
	err := m.eventAdd(events)
	if err != nil {
		m.debug("[StopPlaying] failed to enqueue event: ", err)
		return err
	}

	return nil
}

// StopPlayingAll 메서드는 모든 이벤트를 중지시킨다
// TODO: 즉시 중지시킬지, Queue에 넣어서 순차적으로 처리할지 고민을 좀 해봐야함.
func (m *BHapticsManager) StopPlayingAny() error {
	m.Lock()
	defer m.Unlock()

	if m.connection == nil || !m.IsConnected {
		m.debug("[StopPlayingAny] websocket is not connected")
		return errors.New("websocket is not connected")
	}

	events := []eventRequest{
		{
			Type: "turnOffAll",
		},
	}
	err := m.eventAdd(events)
	if err != nil {
		m.debug("[StopPlayingAny] failed to enqueue event: ", err)
		return err
	}

	return nil
}

// IsPatternRegistered 메서드는 key에 해당하는 패턴이
// 이미 등록이 되어있는지 여부를 반환한다
func (m *BHapticsManager) IsPatternRegistered(key string) (bool, error) {
	m.Lock()
	defer m.Unlock()

	if m.connection == nil || !m.IsConnected {
		m.debug("[IsPatternRegistered] websocket is not connected")
		return false, errors.New("websocket is not connected")
	}

	if !m.connection.lastResponse.isReady {
		m.debug("[IsPatternRegistered] response is not ready")
		return false, errors.New("response is not ready")
	}

	for _, regKey := range m.connection.lastResponse.RegisteredKeys {
		if regKey == key {
			return true, nil
		}
	}

	m.debug("[IsPatternRegistered] key not found")
	return false, errors.New("[IsPatternRegistered] key not found")
}

// TODO
func (m *BHapticsManager) Play(key string, durationMillis int, position bHapticsPosition) {}

// debug 메서드는 debugMode가 true일 때, message를 로그로 출력한다
func (m *BHapticsManager) debug(message ...any) {
	if m.debugMode {
		log.Println(message...)
	}
}
