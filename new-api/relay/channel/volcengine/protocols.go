package volcengine

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/gorilla/websocket"
)

type (
	EventType         int32
	MsgType           uint8
	MsgTypeFlagBits   uint8
	VersionBits       uint8
	HeaderSizeBits    uint8
	SerializationBits uint8
	CompressionBits   uint8
)

const (
	MsgTypeFlagNoSeq       MsgTypeFlagBits = 0
	MsgTypeFlagPositiveSeq MsgTypeFlagBits = 0b1
	MsgTypeFlagNegativeSeq MsgTypeFlagBits = 0b11
	MsgTypeFlagWithEvent   MsgTypeFlagBits = 0b100
)

const (
	Version1 VersionBits = iota + 1
)

const (
	HeaderSize4 HeaderSizeBits = iota + 1
)

const (
	SerializationJSON SerializationBits = 0b1
)

const (
	CompressionNone CompressionBits = 0
)

const (
	MsgTypeFullClientRequest    MsgType = 0b1
	MsgTypeAudioOnlyClient      MsgType = 0b10
	MsgTypeFullServerResponse   MsgType = 0b1001
	MsgTypeAudioOnlyServer      MsgType = 0b1011
	MsgTypeFrontEndResultServer MsgType = 0b1100
	MsgTypeError                MsgType = 0b1111
)

func (t MsgType) String() string {
	switch t {
	case MsgTypeFullClientRequest:
		return "MsgType_FullClientRequest"
	case MsgTypeAudioOnlyClient:
		return "MsgType_AudioOnlyClient"
	case MsgTypeFullServerResponse:
		return "MsgType_FullServerResponse"
	case MsgTypeAudioOnlyServer:
		return "MsgType_AudioOnlyServer"
	case MsgTypeError:
		return "MsgType_Error"
	case MsgTypeFrontEndResultServer:
		return "MsgType_FrontEndResultServer"
	default:
		return fmt.Sprintf("MsgType_(%d)", t)
	}
}

const (
	EventType_None EventType = 0

	EventType_StartConnection  EventType = 1
	EventType_FinishConnection EventType = 2

	EventType_ConnectionStarted  EventType = 50
	EventType_ConnectionFailed   EventType = 51
	EventType_ConnectionFinished EventType = 52

	EventType_StartSession  EventType = 100
	EventType_CancelSession EventType = 101
	EventType_FinishSession EventType = 102

	EventType_SessionStarted  EventType = 150
	EventType_SessionCanceled EventType = 151
	EventType_SessionFinished EventType = 152
	EventType_SessionFailed   EventType = 153

	EventType_UsageResponse EventType = 154

	EventType_TaskRequest  EventType = 200
	EventType_UpdateConfig EventType = 201

	EventType_AudioMuted EventType = 250

	EventType_SayHello EventType = 300

	EventType_TTSSentenceStart     EventType = 350
	EventType_TTSSentenceEnd       EventType = 351
	EventType_TTSResponse          EventType = 352
	EventType_TTSEnded             EventType = 359
	EventType_PodcastRoundStart    EventType = 360
	EventType_PodcastRoundResponse EventType = 361
	EventType_PodcastRoundEnd      EventType = 362

	EventType_ASRInfo     EventType = 450
	EventType_ASRResponse EventType = 451
	EventType_ASREnded    EventType = 459

	EventType_ChatTTSText EventType = 500

	EventType_ChatResponse EventType = 550
	EventType_ChatEnded    EventType = 559

	EventType_SourceSubtitleStart    EventType = 650
	EventType_SourceSubtitleResponse EventType = 651
	EventType_SourceSubtitleEnd      EventType = 652

	EventType_TranslationSubtitleStart    EventType = 653
	EventType_TranslationSubtitleResponse EventType = 654
	EventType_TranslationSubtitleEnd      EventType = 655
)

func (t EventType) String() string {
	switch t {
	case EventType_None:
		return "EventType_None"
	case EventType_StartConnection:
		return "EventType_StartConnection"
	case EventType_FinishConnection:
		return "EventType_FinishConnection"
	case EventType_ConnectionStarted:
		return "EventType_ConnectionStarted"
	case EventType_ConnectionFailed:
		return "EventType_ConnectionFailed"
	case EventType_ConnectionFinished:
		return "EventType_ConnectionFinished"
	case EventType_StartSession:
		return "EventType_StartSession"
	case EventType_CancelSession:
		return "EventType_CancelSession"
	case EventType_FinishSession:
		return "EventType_FinishSession"
	case EventType_SessionStarted:
		return "EventType_SessionStarted"
	case EventType_SessionCanceled:
		return "EventType_SessionCanceled"
	case EventType_SessionFinished:
		return "EventType_SessionFinished"
	case EventType_SessionFailed:
		return "EventType_SessionFailed"
	case EventType_UsageResponse:
		return "EventType_UsageResponse"
	case EventType_TaskRequest:
		return "EventType_TaskRequest"
	case EventType_UpdateConfig:
		return "EventType_UpdateConfig"
	case EventType_AudioMuted:
		return "EventType_AudioMuted"
	case EventType_SayHello:
		return "EventType_SayHello"
	case EventType_TTSSentenceStart:
		return "EventType_TTSSentenceStart"
	case EventType_TTSSentenceEnd:
		return "EventType_TTSSentenceEnd"
	case EventType_TTSResponse:
		return "EventType_TTSResponse"
	case EventType_TTSEnded:
		return "EventType_TTSEnded"
	case EventType_PodcastRoundStart:
		return "EventType_PodcastRoundStart"
	case EventType_PodcastRoundResponse:
		return "EventType_PodcastRoundResponse"
	case EventType_PodcastRoundEnd:
		return "EventType_PodcastRoundEnd"
	case EventType_ASRInfo:
		return "EventType_ASRInfo"
	case EventType_ASRResponse:
		return "EventType_ASRResponse"
	case EventType_ASREnded:
		return "EventType_ASREnded"
	case EventType_ChatTTSText:
		return "EventType_ChatTTSText"
	case EventType_ChatResponse:
		return "EventType_ChatResponse"
	case EventType_ChatEnded:
		return "EventType_ChatEnded"
	case EventType_SourceSubtitleStart:
		return "EventType_SourceSubtitleStart"
	case EventType_SourceSubtitleResponse:
		return "EventType_SourceSubtitleResponse"
	case EventType_SourceSubtitleEnd:
		return "EventType_SourceSubtitleEnd"
	case EventType_TranslationSubtitleStart:
		return "EventType_TranslationSubtitleStart"
	case EventType_TranslationSubtitleResponse:
		return "EventType_TranslationSubtitleResponse"
	case EventType_TranslationSubtitleEnd:
		return "EventType_TranslationSubtitleEnd"
	default:
		return fmt.Sprintf("EventType_(%d)", t)
	}
}

type Message struct {
	Version       VersionBits
	HeaderSize    HeaderSizeBits
	MsgType       MsgType
	MsgTypeFlag   MsgTypeFlagBits
	Serialization SerializationBits
	Compression   CompressionBits

	EventType EventType
	SessionID string
	ConnectID string
	Sequence  int32
	ErrorCode uint32

	Payload []byte
}

func NewMessageFromBytes(data []byte) (*Message, error) {
	if len(data) < 3 {
		return nil, fmt.Errorf("data too short: expected at least 3 bytes, got %d", len(data))
	}

	typeAndFlag := data[1]

	msg, err := NewMessage(MsgType(typeAndFlag>>4), MsgTypeFlagBits(typeAndFlag&0b00001111))
	if err != nil {
		return nil, err
	}

	if err := msg.Unmarshal(data); err != nil {
		return nil, err
	}

	return msg, nil
}

func NewMessage(msgType MsgType, flag MsgTypeFlagBits) (*Message, error) {
	return &Message{
		MsgType:       msgType,
		MsgTypeFlag:   flag,
		Version:       Version1,
		HeaderSize:    HeaderSize4,
		Serialization: SerializationJSON,
		Compression:   CompressionNone,
	}, nil
}

func (m *Message) String() string {
	switch m.MsgType {
	case MsgTypeAudioOnlyServer, MsgTypeAudioOnlyClient:
		if m.MsgTypeFlag == MsgTypeFlagPositiveSeq || m.MsgTypeFlag == MsgTypeFlagNegativeSeq {
			return fmt.Sprintf("%s, %s, Sequence: %d, PayloadSize: %d", m.MsgType, m.EventType, m.Sequence, len(m.Payload))
		}
		return fmt.Sprintf("%s, %s, PayloadSize: %d", m.MsgType, m.EventType, len(m.Payload))
	case MsgTypeError:
		return fmt.Sprintf("%s, %s, ErrorCode: %d, Payload: %s", m.MsgType, m.EventType, m.ErrorCode, string(m.Payload))
	default:
		if m.MsgTypeFlag == MsgTypeFlagPositiveSeq || m.MsgTypeFlag == MsgTypeFlagNegativeSeq {
			return fmt.Sprintf("%s, %s, Sequence: %d, Payload: %s",
				m.MsgType, m.EventType, m.Sequence, string(m.Payload))
		}
		return fmt.Sprintf("%s, %s, Payload: %s", m.MsgType, m.EventType, string(m.Payload))
	}
}

func (m *Message) Marshal() ([]byte, error) {
	buf := new(bytes.Buffer)

	header := []uint8{
		uint8(m.Version)<<4 | uint8(m.HeaderSize),
		uint8(m.MsgType)<<4 | uint8(m.MsgTypeFlag),
		uint8(m.Serialization)<<4 | uint8(m.Compression),
	}

	headerSize := 4 * int(m.HeaderSize)
	if padding := headerSize - len(header); padding > 0 {
		header = append(header, make([]uint8, padding)...)
	}

	if err := binary.Write(buf, binary.BigEndian, header); err != nil {
		return nil, err
	}

	writers, err := m.writers()
	if err != nil {
		return nil, err
	}

	for _, write := range writers {
		if err := write(buf); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

func (m *Message) Unmarshal(data []byte) error {
	buf := bytes.NewBuffer(data)

	versionAndHeaderSize, err := buf.ReadByte()
	if err != nil {
		return err
	}

	m.Version = VersionBits(versionAndHeaderSize >> 4)
	m.HeaderSize = HeaderSizeBits(versionAndHeaderSize & 0b00001111)

	_, err = buf.ReadByte()
	if err != nil {
		return err
	}

	serializationCompression, err := buf.ReadByte()
	if err != nil {
		return err
	}

	m.Serialization = SerializationBits(serializationCompression & 0b11110000)
	m.Compression = CompressionBits(serializationCompression & 0b00001111)

	headerSize := 4 * int(m.HeaderSize)
	readSize := 3
	if paddingSize := headerSize - readSize; paddingSize > 0 {
		if n, err := buf.Read(make([]byte, paddingSize)); err != nil || n < paddingSize {
			return fmt.Errorf("insufficient header bytes: expected %d, got %d", paddingSize, n)
		}
	}

	readers, err := m.readers()
	if err != nil {
		return err
	}

	for _, read := range readers {
		if err := read(buf); err != nil {
			return err
		}
	}

	if _, err := buf.ReadByte(); err != io.EOF {
		return fmt.Errorf("unexpected data after message: %v", err)
	}

	return nil
}

func (m *Message) writers() (writers []func(*bytes.Buffer) error, _ error) {
	if m.MsgTypeFlag == MsgTypeFlagWithEvent {
		writers = append(writers, m.writeEvent, m.writeSessionID)
	}

	switch m.MsgType {
	case MsgTypeFullClientRequest, MsgTypeFullServerResponse, MsgTypeFrontEndResultServer, MsgTypeAudioOnlyClient, MsgTypeAudioOnlyServer:
		if m.MsgTypeFlag == MsgTypeFlagPositiveSeq || m.MsgTypeFlag == MsgTypeFlagNegativeSeq {
			writers = append(writers, m.writeSequence)
		}
	case MsgTypeError:
		writers = append(writers, m.writeErrorCode)
	default:
		return nil, fmt.Errorf("unsupported message type: %d", m.MsgType)
	}

	writers = append(writers, m.writePayload)
	return writers, nil
}

func (m *Message) writeEvent(buf *bytes.Buffer) error {
	return binary.Write(buf, binary.BigEndian, m.EventType)
}

func (m *Message) writeSessionID(buf *bytes.Buffer) error {
	switch m.EventType {
	case EventType_StartConnection, EventType_FinishConnection,
		EventType_ConnectionStarted, EventType_ConnectionFailed:
		return nil
	}

	size := len(m.SessionID)
	if int64(size) > math.MaxUint32 {
		return fmt.Errorf("session ID size (%d) exceeds max(uint32)", size)
	}

	if err := binary.Write(buf, binary.BigEndian, uint32(size)); err != nil {
		return err
	}

	buf.WriteString(m.SessionID)
	return nil
}

func (m *Message) writeSequence(buf *bytes.Buffer) error {
	return binary.Write(buf, binary.BigEndian, m.Sequence)
}

func (m *Message) writeErrorCode(buf *bytes.Buffer) error {
	return binary.Write(buf, binary.BigEndian, m.ErrorCode)
}

func (m *Message) writePayload(buf *bytes.Buffer) error {
	size := len(m.Payload)
	if int64(size) > math.MaxUint32 {
		return fmt.Errorf("payload size (%d) exceeds max(uint32)", size)
	}

	if err := binary.Write(buf, binary.BigEndian, uint32(size)); err != nil {
		return err
	}

	buf.Write(m.Payload)
	return nil
}

func (m *Message) readers() (readers []func(*bytes.Buffer) error, _ error) {
	switch m.MsgType {
	case MsgTypeFullClientRequest, MsgTypeFullServerResponse, MsgTypeFrontEndResultServer, MsgTypeAudioOnlyClient, MsgTypeAudioOnlyServer:
		if m.MsgTypeFlag == MsgTypeFlagPositiveSeq || m.MsgTypeFlag == MsgTypeFlagNegativeSeq {
			readers = append(readers, m.readSequence)
		}
	case MsgTypeError:
		readers = append(readers, m.readErrorCode)
	default:
		return nil, fmt.Errorf("unsupported message type: %d", m.MsgType)
	}

	if m.MsgTypeFlag == MsgTypeFlagWithEvent {
		readers = append(readers, m.readEvent, m.readSessionID, m.readConnectID)
	}

	readers = append(readers, m.readPayload)
	return readers, nil
}

func (m *Message) readEvent(buf *bytes.Buffer) error {
	return binary.Read(buf, binary.BigEndian, &m.EventType)
}

func (m *Message) readSessionID(buf *bytes.Buffer) error {
	switch m.EventType {
	case EventType_StartConnection, EventType_FinishConnection,
		EventType_ConnectionStarted, EventType_ConnectionFailed,
		EventType_ConnectionFinished:
		return nil
	}

	var size uint32
	if err := binary.Read(buf, binary.BigEndian, &size); err != nil {
		return err
	}

	if size > 0 {
		m.SessionID = string(buf.Next(int(size)))
	}

	return nil
}

func (m *Message) readConnectID(buf *bytes.Buffer) error {
	switch m.EventType {
	case EventType_ConnectionStarted, EventType_ConnectionFailed,
		EventType_ConnectionFinished:
	default:
		return nil
	}

	var size uint32
	if err := binary.Read(buf, binary.BigEndian, &size); err != nil {
		return err
	}

	if size > 0 {
		m.ConnectID = string(buf.Next(int(size)))
	}

	return nil
}

func (m *Message) readSequence(buf *bytes.Buffer) error {
	return binary.Read(buf, binary.BigEndian, &m.Sequence)
}

func (m *Message) readErrorCode(buf *bytes.Buffer) error {
	return binary.Read(buf, binary.BigEndian, &m.ErrorCode)
}

func (m *Message) readPayload(buf *bytes.Buffer) error {
	var size uint32
	if err := binary.Read(buf, binary.BigEndian, &size); err != nil {
		return err
	}

	if size > 0 {
		m.Payload = buf.Next(int(size))
	}

	return nil
}

func ReceiveMessage(conn *websocket.Conn) (*Message, error) {
	mt, frame, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	if mt != websocket.BinaryMessage && mt != websocket.TextMessage {
		return nil, fmt.Errorf("unexpected Websocket message type: %d", mt)
	}
	msg, err := NewMessageFromBytes(frame)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

func FullClientRequest(conn *websocket.Conn, payload []byte) error {
	msg, err := NewMessage(MsgTypeFullClientRequest, MsgTypeFlagNoSeq)
	if err != nil {
		return err
	}
	msg.Payload = payload
	frame, err := msg.Marshal()
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.BinaryMessage, frame)
}
