package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"net"
)

// handleOpen обрабатывает открытие файла
func (s *TCPServer) handleOpen(conn net.Conn, req Request) {
	var params OpenParams
	if err := gob.NewDecoder(bytes.NewReader(req.Params)).Decode(&params); err != nil {
		s.sendError(conn, req.ID, fmt.Sprintf("ошибка: %v", err))
		return
	}

	s.openMu.Lock()
	if len(s.openFiles) >= MAX_OPEN_FILES {
		s.openMu.Unlock()
		s.sendError(conn, req.ID, "превышен лимит открытых файлов")
		return
	}
	s.openFiles[params.Filename]++
	s.openMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if handler, exists := s.handlers[params.Filename]; exists {
		data, err := handler.GetData()
		if err != nil {
			s.sendError(conn, req.ID, err.Error())
			return
		}
		s.sendSuccessGob(conn, req.ID, data)
		return
	}

	handler, err := NewFAT16Handler(params.Filename)
	if err != nil {
		s.sendError(conn, req.ID, fmt.Sprintf("ошибка: %v", err))
		return
	}

	s.handlers[params.Filename] = handler
	data, err := handler.GetData()
	if err != nil {
		s.sendError(conn, req.ID, err.Error())
		return
	}
	s.sendSuccessGob(conn, req.ID, data)
}

// handleVisualize обрабатывает запрос визуализации
func (s *TCPServer) handleVisualize(conn net.Conn, req Request) {
	var params VisualizeParams
	if err := gob.NewDecoder(bytes.NewReader(req.Params)).Decode(&params); err != nil {
		s.sendError(conn, req.ID, fmt.Sprintf("ошибка: %v", err))
		return
	}

	s.mu.RLock()
	handler, exists := s.handlers[params.Filename]
	s.mu.RUnlock()

	if !exists {
		s.sendError(conn, req.ID, "файл не открыт")
		return
	}

	vis, err := handler.GetVisualization()
	if err != nil {
		s.sendError(conn, req.ID, err.Error())
		return
	}

	s.sendSuccessGob(conn, req.ID, VisualizeResult{Visualization: vis})
}

// handleCreateDamage обрабатывает создание повреждения
func (s *TCPServer) handleCreateDamage(conn net.Conn, req Request) {
	var params CreateDamageParams
	if err := gob.NewDecoder(bytes.NewReader(req.Params)).Decode(&params); err != nil {
		s.sendError(conn, req.ID, fmt.Sprintf("ошибка: %v", err))
		return
	}

	s.mu.RLock()
	handler, exists := s.handlers[params.Filename]
	s.mu.RUnlock()

	if !exists {
		s.sendError(conn, req.ID, "файл не открыт")
		return
	}

	damage, err := handler.CreateDamage(params.DamageType)
	if err != nil {
		s.sendError(conn, req.ID, err.Error())
		return
	}

	s.sendSuccessGob(conn, req.ID, damage)
}

// handleCheck обрабатывает проверку и исправление ФС
func (s *TCPServer) handleCheck(conn net.Conn, req Request) {
	var params CheckParams
	if err := gob.NewDecoder(bytes.NewReader(req.Params)).Decode(&params); err != nil {
		s.sendError(conn, req.ID, fmt.Sprintf("ошибка: %v", err))
		return
	}

	s.mu.RLock()
	handler, exists := s.handlers[params.Filename]
	s.mu.RUnlock()

	if !exists {
		s.sendError(conn, req.ID, "файл не открыт")
		return
	}

	result, fixes, err := handler.CheckAndFix()
	if err != nil {
		s.sendError(conn, req.ID, err.Error())
		return
	}

	s.sendSuccessGob(conn, req.ID, CheckResponse{Result: *result, Fixes: fixes})
}

// handleClose обрабатывает закрытие файла
func (s *TCPServer) handleClose(conn net.Conn, req Request) {
	var params CloseParams
	if err := gob.NewDecoder(bytes.NewReader(req.Params)).Decode(&params); err != nil {
		s.sendError(conn, req.ID, fmt.Sprintf("ошибка: %v", err))
		return
	}

	s.openMu.Lock()
	if count, exists := s.openFiles[params.Filename]; exists {
		if count <= 1 {
			delete(s.openFiles, params.Filename)
		} else {
			s.openFiles[params.Filename] = count - 1
		}
	}
	s.openMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if handler, exists := s.handlers[params.Filename]; exists {
		s.openMu.Lock()
		_, stillOpen := s.openFiles[params.Filename]
		s.openMu.Unlock()

		if !stillOpen {
			handler.Close()
			delete(s.handlers, params.Filename)
		}
	}

	s.sendSuccessGob(conn, req.ID, struct{}{})
}
