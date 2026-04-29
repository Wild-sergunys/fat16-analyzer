package main

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// TCPServer TCP сервер
type TCPServer struct {
	handlers  map[string]*FAT16Handler
	mu        sync.RWMutex
	listener  net.Listener
	wg        sync.WaitGroup
	stopChan  chan struct{}
	openFiles map[string]int
	openMu    sync.Mutex
}

// NewTCPServer создает новый TCP сервер
func NewTCPServer() *TCPServer {
	return &TCPServer{
		handlers:  make(map[string]*FAT16Handler),
		stopChan:  make(chan struct{}),
		openFiles: make(map[string]int),
	}
}

// Start запускает сервер
func (s *TCPServer) Start(port int) error {
	var err error
	s.listener, err = net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return fmt.Errorf("ошибка: %v", err)
	}

	fmt.Printf("🚀 Сервер запущен на порту %d\n", port)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n🛑 Остановка сервера...")
		s.Stop()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.stopChan:
				return nil
			default:
				continue
			}
		}
		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

// Stop останавливает сервер
func (s *TCPServer) Stop() {
	close(s.stopChan)
	if s.listener != nil {
		s.listener.Close()
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}

	s.mu.Lock()
	for _, handler := range s.handlers {
		handler.Close()
	}
	s.handlers = make(map[string]*FAT16Handler)
	s.mu.Unlock()
}

// handleConnection обрабатывает одно TCP соединение
func (s *TCPServer) handleConnection(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(READ_TIMEOUT))

	for {
		select {
		case <-s.stopChan:
			return
		default:
		}

		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return
		}
		msgLen := binary.BigEndian.Uint32(lenBuf)

		if msgLen > MAX_MSG_SIZE {
			s.sendError(conn, 0, "сообщение слишком большое")
			continue
		}

		msgBuf := make([]byte, msgLen)
		if _, err := io.ReadFull(conn, msgBuf); err != nil {
			return
		}

		var req Request
		if err := gob.NewDecoder(bytes.NewReader(msgBuf)).Decode(&req); err != nil {
			s.sendError(conn, 0, fmt.Sprintf("ошибка декодирования: %v", err))
			continue
		}

		s.handleRequest(conn, req)
		conn.SetReadDeadline(time.Now().Add(READ_TIMEOUT))
	}
}

// handleRequest маршрутизирует запрос
func (s *TCPServer) handleRequest(conn net.Conn, req Request) {
	switch req.Method {
	case "open":
		s.handleOpen(conn, req)
	case "visualize":
		s.handleVisualize(conn, req)
	case "create_damage":
		s.handleCreateDamage(conn, req)
	case "check":
		s.handleCheck(conn, req)
	case "close":
		s.handleClose(conn, req)
	default:
		s.sendError(conn, req.ID, fmt.Sprintf("неизвестный метод: %s", req.Method))
	}
}

// sendSuccessGob отправляет успешный ответ
func (s *TCPServer) sendSuccessGob(conn net.Conn, id uint32, data interface{}) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(data); err != nil {
		s.sendError(conn, id, fmt.Sprintf("ошибка кодирования: %v", err))
		return
	}

	resp := Response{ID: id, Result: buf.Bytes()}
	var respBuf bytes.Buffer
	gob.NewEncoder(&respBuf).Encode(resp)

	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(respBuf.Len()))
	conn.Write(lenBuf)
	conn.Write(respBuf.Bytes())
}

// sendError отправляет ответ с ошибкой
func (s *TCPServer) sendError(conn net.Conn, id uint32, errMsg string) {
	resp := Response{ID: id, Error: errMsg}
	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(resp)

	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(buf.Len()))
	conn.Write(lenBuf)
	conn.Write(buf.Bytes())
}
