package main

/*
#cgo CFLAGS: -I..
#cgo LDFLAGS: -L.. -lfat16
#include "fat16.h"
#include <stdlib.h>
*/
import "C"
import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// =======================================
// СТРУКТУРЫ ДАННЫХ ДЛЯ ОБМЕНА С КЛИЕНТОМ
// =======================================

// FAT16Data данные FAT16 для передачи клиенту
type FAT16Data struct {
	FatTable []uint16   `json:"fat_table"`
	Files    []FileInfo `json:"files"`
}

// FileInfo информация о файле
type FileInfo struct {
	Name         string `json:"name"`
	StartCluster int    `json:"start_cluster"`
	Size         int    `json:"size"`
	IsDirectory  bool   `json:"is_directory"`
}

// Damage повреждение (для передачи клиенту)
type Damage struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Cluster     int    `json:"cluster"`
	OldValue    uint16 `json:"old_value"`
	NewValue    uint16 `json:"new_value"`
}

// Intersection пересечение кластеров
type Intersection struct {
	Cluster int      `json:"cluster"`
	Files   []string `json:"files"`
}

// Loop зацикленность
type Loop struct {
	StartCluster int    `json:"start_cluster"`
	FileName     string `json:"file_name"`
}

// CheckResult результат проверки ФС
type CheckResult struct {
	MissingEOF    []int          `json:"missing_eof"`
	Intersections []Intersection `json:"intersections"`
	Loops         []Loop         `json:"loops"`
}

// Request запрос от клиента
type Request struct {
	Method string `json:"method"` // имя метода: open, visualize, create_damage, check, close
	ID     uint32 `json:"id"`     // уникальный ID запроса для сопоставления с ответом
	Params []byte `json:"params"` // параметры запроса в gob-формате
}

// Response ответ сервера
type Response struct {
	ID     uint32 `json:"id"`     // ID запроса, на который отвечаем
	Result []byte `json:"result"` // результат выполнения в gob-формате
	Error  string `json:"error"`  // сообщение об ошибке (если есть)
}

// =========================================
// СТРУКТУРЫ ПАРАМЕТРОВ ДЛЯ РАЗНЫХ МЕТОДОВ
// =========================================

// OpenParams параметры для открытия файла
type OpenParams struct {
	Filename string `json:"filename"`
}

// VisualizeParams параметры для визуализации
type VisualizeParams struct {
	Filename string `json:"filename"`
}

// VisualizeResult результат визуализации
type VisualizeResult struct {
	Visualization string `json:"visualization"`
}

// CreateDamageParams параметры для создания повреждения
type CreateDamageParams struct {
	Filename   string `json:"filename"`
	DamageType string `json:"damage_type"` // missing_eof, intersection, loop
}

// CheckParams параметры для проверки ФС
type CheckParams struct {
	Filename string `json:"filename"`
}

// CloseParams параметры для закрытия файла
type CloseParams struct {
	Filename string `json:"filename"`
}

// CheckResponse результат проверки с исправлениями
type CheckResponse struct {
	Result CheckResult `json:"result"`
	Fixes  []Damage    `json:"fixes"`
}

// =======================================
// РЕГИСТРАЦИЯ ТИПОВ ДЛЯ GOB СЕРИАЛИЗАЦИИ
// =======================================

func init() {
	// Регистрируем все типы, которые будут передаваться через gob
	gob.Register(&FAT16Data{})
	gob.Register(&Damage{})
	gob.Register(&CheckResult{})
	gob.Register(&FileInfo{})
	gob.Register(&Intersection{})
	gob.Register(&Loop{})
	gob.Register(&OpenParams{})
	gob.Register(&VisualizeParams{})
	gob.Register(&CreateDamageParams{})
	gob.Register(&CheckParams{})
	gob.Register(&CloseParams{})
	gob.Register(&VisualizeResult{})
	gob.Register(&CheckResponse{})
}

// ==================
// КОНСТАНТЫ СЕРВЕРА
// ==================

const (
	MAX_MSG_SIZE   = 10 * 1024 * 1024 // максимальный размер сообщения (10 MB)
	READ_TIMEOUT   = 20 * time.Minute // таймаут чтения
	MAX_OPEN_FILES = 100              // максимальное количество одновременно открытых файлов
)

// =====================================
// FAT16HANDLER — ОБЕРТКА НАД C LIBRARY
// =====================================

// FAT16Handler обработчик FAT16 через C библиотеку
type FAT16Handler struct {
	mu           sync.RWMutex // защита от конкурентного доступа
	filename     string       // имя файла образа
	handle       int          // handle экземпляра FAT16 в библиотеке
	lastModified time.Time    // время последнего изменения файла
}

// NewFAT16Handler создает новый обработчик, загружая образ через C библиотеку
func NewFAT16Handler(filename string) (*FAT16Handler, error) {
	// Преобразуем строку в C-строку
	cFilename := C.CString(filename)
	defer C.free(unsafe.Pointer(cFilename))

	// Вызываем C функцию LoadFAT16 из библиотеки
	handle := int(C.LoadFAT16(cFilename))
	if handle < 0 {
		return nil, fmt.Errorf("ошибка загрузки FAT16: %d", handle)
	}

	h := &FAT16Handler{
		filename: filename,
		handle:   handle,
	}

	return h, nil
}

// GetData получает данные FAT16 (FAT таблицу и список файлов) из библиотеки
func (h *FAT16Handler) GetData() (*FAT16Data, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Получаем список файлов в JSON формате
	filesBuf := make([]byte, 1024*1024) // буфер 1 MB
	size := C.GetFiles(C.int(h.handle), (*C.char)(unsafe.Pointer(&filesBuf[0])), C.int(len(filesBuf)))
	if size < 0 {
		return nil, fmt.Errorf("ошибка получения файлов: %d", size)
	}

	var files []FileInfo
	if err := json.Unmarshal(filesBuf[:size], &files); err != nil {
		return nil, fmt.Errorf("ошибка парсинга файлов: %v", err)
	}

	// Получаем FAT таблицу в JSON формате
	fatBuf := make([]byte, 1024*1024) // буфер 1 MB
	size = C.GetFATTable(C.int(h.handle), (*C.char)(unsafe.Pointer(&fatBuf[0])), C.int(len(fatBuf)))
	if size < 0 {
		return nil, fmt.Errorf("ошибка получения FAT таблицы: %d", size)
	}

	var fatTable []uint16
	if err := json.Unmarshal(fatBuf[:size], &fatTable); err != nil {
		return nil, fmt.Errorf("ошибка парсинга FAT таблицы: %v", err)
	}

	return &FAT16Data{
		FatTable: fatTable,
		Files:    files,
	}, nil
}

// GetVisualization получает текстовую визуализацию файловой системы
func (h *FAT16Handler) GetVisualization() (string, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	data, err := h.GetData()
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("╔════════════════════════════════════════════════════════════╗\n")
	sb.WriteString("                     ВИЗУАЛИЗАЦИЯ FAT16                       \n")
	sb.WriteString("╚════════════════════════════════════════════════════════════╝\n\n")

	// Статистика использования кластеров
	total := len(data.FatTable) - 2
	used, free := 0, 0
	for i := 2; i < len(data.FatTable); i++ {
		if data.FatTable[i] == 0 {
			free++
		} else if data.FatTable[i] != 0 {
			used++
		}
	}

	sb.WriteString(fmt.Sprintf("📊 Статистика:\n%s\n", strings.Repeat("─", 40)))
	sb.WriteString(fmt.Sprintf("📁 Файлов: %d\n", len(data.Files)))
	sb.WriteString(fmt.Sprintf("📊 Кластеров: %d\n", total))
	sb.WriteString(fmt.Sprintf("📝 Занято: %d (%.1f%%)\n", used, float64(used)/float64(total)*100))
	sb.WriteString(fmt.Sprintf("⬜ Свободно: %d (%.1f%%)\n\n", free, float64(free)/float64(total)*100))

	sb.WriteString("📁 Файлы:\n")
	sb.WriteString(strings.Repeat("─", 40) + "\n")

	// Получение цепочки с обнаружением циклов
	type ChainResult struct {
		Chain   []int
		HasLoop bool
		LoopAt  int
	}

	getChainWithLoopDetection := func(startCluster int) ChainResult {
		var chain []int
		visited := make(map[int]int)
		current := startCluster

		for current >= 2 && current < len(data.FatTable) {
			if pos, ok := visited[current]; ok {
				// Нашли цикл!
				return ChainResult{
					Chain:   chain,
					HasLoop: true,
					LoopAt:  pos,
				}
			}
			visited[current] = len(chain)
			chain = append(chain, current)

			val := data.FatTable[current]
			if val == 0xFFFF || val >= 0xFFF8 {
				break
			}
			if val == 0 || int(val) < 2 || int(val) >= len(data.FatTable) {
				break
			}
			current = int(val)
		}
		return ChainResult{Chain: chain, HasLoop: false}
	}

	// Выводим информацию о каждом файле
	for _, f := range data.Files {
		emoji := "📄"
		if f.IsDirectory {
			emoji = "📂"
		}

		// Форматируем размер в читаемый вид
		sizeStr := fmt.Sprintf("%d байт", f.Size)
		if f.Size > 1024 {
			sizeStr = fmt.Sprintf("%.1f KB", float64(f.Size)/1024)
		}
		if f.Size > 1024*1024 {
			sizeStr = fmt.Sprintf("%.1f MB", float64(f.Size)/(1024*1024))
		}

		sb.WriteString(fmt.Sprintf("%s %s (%s)\n", emoji, f.Name, sizeStr))
		sb.WriteString(fmt.Sprintf("   📍 Стартовый кластер: %d\n", f.StartCluster))

		// Получаем цепочку с обнаружением циклов
		chainResult := getChainWithLoopDetection(f.StartCluster)

		if chainResult.HasLoop {
			// Отображаем цикл
			sb.WriteString("   🔁 ⚠️ ОБНАРУЖЕН ЦИКЛ! ⚠️\n")
			sb.WriteString("   🔗 Цепочка до цикла: ")
			for i, c := range chainResult.Chain {
				if i > 0 {
					sb.WriteString(" → ")
				}
				sb.WriteString(fmt.Sprintf("%d", c))
			}
			sb.WriteString("\n")
			sb.WriteString(fmt.Sprintf("   🔄 Зацикливание: кластер %d указывает на уже посещенный кластер %d\n",
				chainResult.Chain[len(chainResult.Chain)-1], chainResult.Chain[chainResult.LoopAt]))
			sb.WriteString("   💡 Для исправления нажмите 'Проверить и исправить'\n")
		} else if len(chainResult.Chain) > 0 {
			// Нормальная цепочка
			sb.WriteString("   🔗 Цепочка кластеров: ")
			for i, c := range chainResult.Chain {
				if i > 0 {
					sb.WriteString(" → ")
				}
				val := data.FatTable[c]
				if i == len(chainResult.Chain)-1 && (val == 0xFFFF || val >= 0xFFF8) {
					sb.WriteString(fmt.Sprintf("%d [EOF]", c))
				} else {
					sb.WriteString(fmt.Sprintf("%d", c))
				}
			}
			sb.WriteString("\n")
		} else if f.StartCluster >= 2 && f.StartCluster < len(data.FatTable) {
			val := data.FatTable[f.StartCluster]
			if val == 0xFFFF || val >= 0xFFF8 {
				sb.WriteString(fmt.Sprintf("   🔗 Цепочка кластеров: %d [EOF]\n", f.StartCluster))
			} else if val != 0 {
				sb.WriteString(fmt.Sprintf("   🔗 Цепочка кластеров: %d → ?\n", f.StartCluster))
			}
		}
		sb.WriteString("\n")
	}

	// Информация о FAT таблице для отладки
	sb.WriteString(strings.Repeat("─", 40) + "\n")
	sb.WriteString("📊 Первые 20 записей FAT таблицы:\n")
	for i := 0; i < 20 && i < len(data.FatTable); i++ {
		val := data.FatTable[i]
		var desc string
		switch {
		case i < 2:
			desc = " (зарезервирован)"
		case val == 0:
			desc = " [свободно]"
		case val == 0xFFFF || val >= 0xFFF8:
			desc = " [EOF]"
		case val == 0xFFF7:
			desc = " [BAD]"
		default:
			desc = fmt.Sprintf(" → %d", val)
		}
		sb.WriteString(fmt.Sprintf("   Кластер %3d: 0x%04X (%5d)%s\n", i, val, val, desc))
	}
	if len(data.FatTable) > 20 {
		sb.WriteString(fmt.Sprintf("   ... и еще %d записей\n", len(data.FatTable)-20))
	}

	return sb.String(), nil
}

// CreateDamage создает повреждение в файловой системе
func (h *FAT16Handler) CreateDamage(damageType string) (*Damage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	cDamageType := C.CString(damageType)
	defer C.free(unsafe.Pointer(cDamageType))

	resultBuf := make([]byte, 64*1024) // буфер 64 KB
	size := C.CreateDamage(C.int(h.handle), cDamageType, (*C.char)(unsafe.Pointer(&resultBuf[0])), C.int(len(resultBuf)))

	if size < 0 {
		return nil, fmt.Errorf("ошибка создания повреждения: %d", size)
	}

	var damage Damage
	if err := json.Unmarshal(resultBuf[:size], &damage); err != nil {
		return nil, fmt.Errorf("ошибка парсинга: %v", err)
	}

	return &damage, nil
}

// CheckAndFix проверяет и исправляет файловую систему
func (h *FAT16Handler) CheckAndFix() (*CheckResult, []Damage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	resultBuf := make([]byte, 1024*1024) // буфер 1 MB
	size := C.CheckFAT(C.int(h.handle), (*C.char)(unsafe.Pointer(&resultBuf[0])), C.int(len(resultBuf)))

	if size < 0 {
		return nil, nil, fmt.Errorf("ошибка проверки: %d", size)
	}

	var response struct {
		MissingEOF    []int          `json:"missing_eof"`
		Intersections []Intersection `json:"intersections"`
		Loops         []Loop         `json:"loops"`
		Fixes         []Damage       `json:"fixes"`
	}

	if err := json.Unmarshal(resultBuf[:size], &response); err != nil {
		return nil, nil, fmt.Errorf("ошибка парсинга: %v", err)
	}

	result := &CheckResult{
		MissingEOF:    response.MissingEOF,
		Intersections: response.Intersections,
		Loops:         response.Loops,
	}

	return result, response.Fixes, nil
}

// Close закрывает обработчик и освобождает ресурсы
func (h *FAT16Handler) Close() {
	C.CloseFAT16(C.int(h.handle))
}

// ===========
// TCP СЕРВЕР
// ===========

// TCPServer TCP сервер для обработки клиентских запросов
type TCPServer struct {
	handlers  map[string]*FAT16Handler // открытые обработчики файлов
	mu        sync.RWMutex             // защита handlers
	listener  net.Listener             // слушатель TCP соединений
	wg        sync.WaitGroup           // ожидание завершения всех горутин
	stopChan  chan struct{}            // канал для остановки сервера
	openFiles map[string]int           // счетчик открытий файлов
	openMu    sync.Mutex               // защита openFiles
}

// NewTCPServer создает новый TCP сервер
func NewTCPServer() *TCPServer {
	return &TCPServer{
		handlers:  make(map[string]*FAT16Handler),
		stopChan:  make(chan struct{}),
		openFiles: make(map[string]int),
	}
}

// Start запускает сервер на указанном порту
func (s *TCPServer) Start(port int) error {
	var err error
	s.listener, err = net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return fmt.Errorf("ошибка: %v", err)
	}

	fmt.Printf("🚀 Сервер запущен на порту %d\n", port)

	/// Обработка сигналов для graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM) // Подписываемся на сигналы os.Interrupt - сигнал прерывания, syscall.SIGTERM - сигнал завершения
	go func() {                                           // Запускаем горутину-слушатель
		<-sigChan                               // Ждем сигнал (блокировка)
		fmt.Println("\n🛑 Остановка сервера...") // Получили сигнал
		s.Stop()                                // Останавливаем сервер
	}()

	// Основной цикл приема соединений
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

// handleConnection обрабатывает одно TCP соединение
func (s *TCPServer) handleConnection(conn net.Conn) {
	defer s.wg.Done()                                  // уменьшаем счетчик WaitGroup (для graceful shutdown)
	defer conn.Close()                                 // закрываем сокет
	conn.SetReadDeadline(time.Now().Add(READ_TIMEOUT)) // таймаут чтения

	for { // бесконечный цикл обработки запросов
		select {
		case <-s.stopChan: // если пришел сигнал остановки
			return // выходим из горутины
		default: // иначе продолжаем
		}

		// Читаем длину сообщения (4 байта, big-endian)
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return // клиент отключился или ошибка
		}
		msgLen := binary.BigEndian.Uint32(lenBuf) // преобразуем в число

		// Проверяем размер (защита от слишком больших сообщений)
		if msgLen > MAX_MSG_SIZE {
			s.sendError(conn, 0, "сообщение слишком большое")
			continue // пропускаем, продолжаем слушать
		}

		// Читаем само сообщение
		msgBuf := make([]byte, msgLen)
		if _, err := io.ReadFull(conn, msgBuf); err != nil {
			return
		}

		// Декодируем запрос из gob-формата
		var req Request
		if err := gob.NewDecoder(bytes.NewReader(msgBuf)).Decode(&req); err != nil {
			s.sendError(conn, 0, fmt.Sprintf("ошибка декодирования: %v", err))
			continue
		}

		// Обрабатываем запрос (open, visualize, create_damage, check, close)
		s.handleRequest(conn, req)

		// Обновляем таймаут для следующего запроса
		conn.SetReadDeadline(time.Now().Add(READ_TIMEOUT))
	}
}

// handleRequest маршрутизирует запрос к обработчику
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

// handleOpen обрабатывает открытие файла
func (s *TCPServer) handleOpen(conn net.Conn, req Request) {
	var params OpenParams
	if err := gob.NewDecoder(bytes.NewReader(req.Params)).Decode(&params); err != nil {
		s.sendError(conn, req.ID, fmt.Sprintf("ошибка: %v", err))
		return
	}

	// Проверяем лимит открытых файлов
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

	// Если файл уже открыт, возвращаем существующий обработчик
	if handler, exists := s.handlers[params.Filename]; exists {
		data, err := handler.GetData()
		if err != nil {
			s.sendError(conn, req.ID, err.Error())
			return
		}
		s.sendSuccessGob(conn, req.ID, data)
		return
	}

	// Создаем новый обработчик
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

	// Уменьшаем счетчик открытий
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

	// Если файл больше не открыт, закрываем обработчик
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

// sendSuccessGob отправляет успешный ответ в формате gob
func (s *TCPServer) sendSuccessGob(conn net.Conn, id uint32, data interface{}) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(data); err != nil {
		s.sendError(conn, id, fmt.Sprintf("ошибка кодирования: %v", err))
		return
	}

	resp := Response{ID: id, Result: buf.Bytes()}
	var respBuf bytes.Buffer
	if err := gob.NewEncoder(&respBuf).Encode(resp); err != nil {
		s.sendError(conn, id, fmt.Sprintf("ошибка: %v", err))
		return
	}

	// Отправляем длину сообщения + само сообщение
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

// Stop останавливает сервер и закрывает все соединения
func (s *TCPServer) Stop() {
	close(s.stopChan)
	if s.listener != nil {
		s.listener.Close()
	}

	// Ожидаем завершения всех обработчиков соединений
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}

	// Закрываем все открытые обработчики файлов
	s.mu.Lock()
	for _, handler := range s.handlers {
		handler.Close()
	}
	s.handlers = make(map[string]*FAT16Handler)
	s.mu.Unlock()
}

// ============
// ТОЧКА ВХОДА
// ============

func main() {
	port := 8080
	if len(os.Args) > 1 {
		if p, err := strconv.Atoi(os.Args[1]); err == nil {
			port = p
		}
	}
	server := NewTCPServer()
	if err := server.Start(port); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		os.Exit(1)
	}
}
