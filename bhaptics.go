/*
   이 라이브러리는 BHaptics API를 Go로 구현한 것입니다.
   참고한 예제
   : https://github.com/HerpDerpinstine/bHapticsLib
   : https://github.com/bHaptics
*/

package bhapticsgo

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/enriquebris/goconcurrentqueue"
	"github.com/gorilla/websocket"
)

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
		eventCache:  make(map[string][]event),
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
	go m.registerSend()

	return nil
}

// connect 메서드는 bHaptics API와 WebSocket 연결을 시도한다
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
			m.connection.read <- []byte(fmt.Sprintf("error : %s", err.Error()))
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
		err := sonic.Unmarshal(message, &response)
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

// eventRequestsAdd 메서드는 Queue에 이벤트를 추가한다
func (m *BHapticsManager) eventRequestsAdd(event []event) error {
	m.Lock()
	defer m.Unlock()

	if m.connection.socket == nil || !m.IsConnected {
		m.debug("[eventAdd] websocket is not connected")
		return fmt.Errorf("websocket is not connected")
	}

	req := eventRequest{
		Submit: event,
	}

	msg, err := sonic.Marshal(req)
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

// eventKeyAdd 메서드는 Queue에 이벤트를 추가한다
func (m *BHapticsManager) eventKeyAdd(key string, altKey ...string) error {
	m.Lock()
	defer m.Unlock()

	if m.connection.socket == nil || !m.IsConnected {
		m.debug("[eventKeyAdd] websocket is not connected")
		return fmt.Errorf("websocket is not connected")
	}

	evtReq := event{
		Key:  key,
		Type: "key",
	}
	if len(altKey) > 0 {
		evtReq.AltKey = altKey[0]
	}

	req := eventRequest{
		Submit: []event{evtReq},
	}

	msg, err := sonic.Marshal(req)
	if err != nil {
		m.debug("[send] failed to marshal JSON: ", err)
		return err
	}

	err = m.eventQueue.Enqueue(msg)
	if err != nil {
		m.debug("[send] failed to enqueue event key request: ", err)
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
		// 여기서 connection을 체크하지 않는 이유는
		// 반복문을 유지하기 위함.
		// 어차피 이 전 함수에서 다 체크하므로

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
	if m.connection == nil || !m.IsConnected {
		m.debug("[StopPlaying] websocket is not connected")
		return errors.New("websocket is not connected")
	}

	events := []event{
		{
			Key:  key,
			Type: "turnOff",
		},
	}
	err := m.eventRequestsAdd(events)
	if err != nil {
		m.debug("[StopPlaying] failed to enqueue event: ", err)
		return err
	}

	return nil
}

// StopPlayingAll 메서드는 모든 이벤트를 중지시킨다
// TODO: 즉시 중지시킬지, Queue에 넣어서 순차적으로 처리할지 고민을 좀 해봐야함.
func (m *BHapticsManager) StopPlayingAny() error {
	if m.connection == nil || !m.IsConnected {
		m.debug("[StopPlayingAny] websocket is not connected")
		return errors.New("websocket is not connected")
	}

	events := []event{
		{
			Type: "turnOffAll",
		},
	}
	err := m.eventRequestsAdd(events)
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

// Play 메서드는 Dot, Path 이벤트를 실행한다
// TODO
func (m *BHapticsManager) Play(key string, opt PlayOption) error {
	if key == "" {
		m.debug("[Play] key is empty")
		return errors.New("key is empty")
	}

	if opt.Position == VestPosition {
		if err := m.Play(fmt.Sprintf("%sFront", key), PlayOption{
			Position:       VestFrontPosition,
			DurationMillis: opt.DurationMillis,
			DotPoints:      opt.DotPoints,
			PathPoints:     opt.PathPoints,
		}); err != nil {
			m.debug("[Play] failed to play front: ", err)
			return err
		}

		if err := m.Play(fmt.Sprintf("%sBack", key), PlayOption{
			Position:       VestBackPosition,
			DurationMillis: opt.DurationMillis,
			DotPoints:      opt.DotPoints,
			PathPoints:     opt.PathPoints,
		}); err != nil {
			m.debug("[Play] failed to play back: ", err)
			return err
		}

		return nil
	}

	evt := event{
		Key:  key,
		Type: "frame",
		Frame: framePayload{
			Position:       opt.Position,
			DurationMillis: opt.DurationMillis,
		},
	}

	if opt.DotPoints != nil && len(opt.DotPoints) > 0 {
		evt.Frame.DotPoints = opt.DotPoints
	}

	if opt.PathPoints != nil && len(opt.PathPoints) > 0 {
		evt.Frame.PathPoints = opt.PathPoints
	}

	err := m.eventRequestsAdd([]event{evt})
	if err != nil {
		m.debug("[Play] failed to enqueue event: ", err)
		return err
	}

	return nil
}

// PlayPattern 메서드는 bHaptics API에 등록된
// key에 해당하는 이벤트를 Queue에 등록한다
func (m *BHapticsManager) PlayPattern(key string, altKey ...string) error {
	if key == "" {
		m.debug("[PlayPattern] key is empty")
		return errors.New("key is empty")
	}

	// TODO - eventCache 언제 씀?
	// if m.eventCache[key] == nil {
	// 	m.debug("[PlayPattern] pattern not found in cache")
	// 	return errors.New("pattern not found in cache")
	// }

	err := m.eventKeyAdd(key, altKey...)
	if err != nil {
		m.debug("[PlayPattern] failed to enqueue event key: ", err)
		return err
	}

	return nil
}

// RegisterPatternFromFile 메서드는 tact 파일을
// bHaptics API에 key로 등록한다
func (m *BHapticsManager) RegisterPatternFromFile(key, tactFilePath string) error {
	if key == "" {
		m.debug("[RegisterPatternFromFile] key is empty")
		return errors.New("key is empty")
	}

	if tactFilePath == "" {
		m.debug("[RegisterPatternFromFile] tact fail path is empty")
		return errors.New("tact fail path is empty")
	}

	f, err := os.ReadFile(tactFilePath)
	if err != nil {
		m.debug("[RegisterPatternFromFile] failed to read tact file: ", err)
		return err
	}

	var reg register
	err = sonic.Unmarshal(f, &reg)
	if err != nil {
		m.debug("[RegisterPatternFromFile] failed to unmarshal tact file: ", err)
		return err
	}

	err = m.registerRequestAdd([]register{reg})
	if err != nil {
		m.debug("[registerPatternFromFile] failed to enqueue register: ", err)
		return err
	}

	return nil
}

// RegisterPatternFromJSON 메서드는 tact json 데이터를
// bHaptics API에 key로 등록한다
func (m *BHapticsManager) RegisterPatternFromJSON(key string, tactJsonData string) error {
	if key == "" {
		m.debug("[RegisterPatternFromJSON] key is empty")
		return errors.New("key is empty")
	}

	if tactJsonData == "" {
		m.debug("[RegisterPatternFromJSON] tact JSON data is empty")
		return errors.New("tact JSON data is empty")
	}

	var reg register
	err := sonic.Unmarshal([]byte(tactJsonData), &reg)
	if err != nil {
		m.debug("[RegisterPatternFromJSON] failed to unmarshal tact JSON data: ", err)
		return err
	}

	err = m.registerRequestAdd([]register{reg})
	if err != nil {
		m.debug("[RegisterPatternFromJSON] failed to enqueue register: ", err)
		return err
	}

	return nil
}

// registerRequestAdd 메서드는 Queue에 패턴을 등록한다
func (m *BHapticsManager) registerRequestAdd(register []register) error {
	if m.connection == nil || !m.IsConnected {
		m.debug("[registerRequestAdd] websocket is not connected")
		return errors.New("websocket is not connected")
	}

	req := registerRequest{
		Register: register,
	}

	msg, err := sonic.Marshal(req)
	if err != nil {
		m.debug("[registerRequestAdd] failed to marshal JSON: ", err)
		return err
	}

	err = m.eventQueue.Enqueue(msg)
	if err != nil {
		m.debug("[registerRequestAdd] failed to enqueue event: ", err)
		return err
	}

	return nil
}

// registerSend 메서드는 계속 대기하면서
// Queue에 등록된 패턴이 있다면 bHaptics API로 전달한다
func (m *BHapticsManager) registerSend() error {
	defer func() {
		m.debug("[register] websocket connection closed")
		m.disconnect()
	}()

	for {
		if m.registerQueue.GetLen() > 0 {
			m.debug("[register] register dequeued")
			item, err := m.registerQueue.Dequeue()
			if err != nil {
				m.debug("[register] failed to dequeue register item: ", err)
			} else {
				m.debug("[register] sending register request")
				m.connection.write <- item.([]byte)
			}
		}

		time.Sleep(500 * time.Millisecond)
	}
}

func (m *BHapticsManager) cachingPattern(key string, events []event) error {
	if key == "" {
		m.debug("[cachingPattern] key is empty")
		return errors.New("key is empty")
	}

	if events == nil || len(events) == 0 {
		m.debug("[cachingPattern] events is empty")
		return errors.New("events is empty")
	}

	m.eventCache[key] = events

	return nil
}

// debug 메서드는 debugMode가 true일 때, message를 로그로 출력한다
func (m *BHapticsManager) debug(message ...any) {
	if m.debugMode {
		log.Println(message...)
	}
}
