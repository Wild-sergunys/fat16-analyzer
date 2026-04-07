package main

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// =================
// СТРУКТУРЫ ДАННЫХ
// =================

// FAT16Data данные FAT16 для отображения
type FAT16Data struct {
	FatTable []uint16
	Files    []FileInfo
}

// FileInfo информация о файле
type FileInfo struct {
	Name         string
	StartCluster int
	Size         int
	IsDirectory  bool
}

// Damage повреждение
type Damage struct {
	Type        string
	Description string
	Cluster     int
	OldValue    uint16
	NewValue    uint16
}

// Intersection пересечение кластеров
type Intersection struct {
	Cluster int
	Files   []string
}

// Loop зацикленность
type Loop struct {
	StartCluster int
	FileName     string
}

// CheckResult результат проверки
type CheckResult struct {
	MissingEOF    []int
	Intersections []Intersection
	Loops         []Loop
}

// LogEntry запись в логе
type LogEntry struct {
	ID        int
	Timestamp time.Time
	Type      string
	Details   string
}

// ===========
// TCP КЛИЕНТ
// ===========

// TCPClient управляет TCP-соединением с сервером
type TCPClient struct {
	conn     net.Conn   // TCP-сокет
	mu       sync.Mutex // защита отправки
	muReader sync.Mutex // защита чтения
	nextID   uint32     // счетчик ID запросов
}

// NewTCPClient создает подключение к серверу
func NewTCPClient(port string) (*TCPClient, error) {
	conn, err := net.Dial("tcp", "localhost:"+port)
	if err != nil {
		return nil, fmt.Errorf("ошибка подключения: %v", err)
	}
	return &TCPClient{conn: conn, nextID: 1}, nil
}

// Close закрывает соединение
func (c *TCPClient) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// call — универсальный метод для вызова методов на сервере
func (c *TCPClient) call(method string, params interface{}) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID
	c.nextID++

	// Сериализуем параметры в gob
	var paramsBuf bytes.Buffer
	enc := gob.NewEncoder(&paramsBuf)
	if err := enc.Encode(params); err != nil {
		return nil, fmt.Errorf("ошибка кодирования: %v", err)
	}

	// Создаем запрос
	req := Request{Method: method, ID: id, Params: paramsBuf.Bytes()}

	// Сериализуем запрос в gob
	var reqBuf bytes.Buffer
	enc = gob.NewEncoder(&reqBuf)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("ошибка кодирования запроса: %v", err)
	}

	// Отправляем: [4 байта: длина] [сообщение]
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(reqBuf.Len()))
	if _, err := c.conn.Write(lenBuf); err != nil {
		return nil, err
	}
	if _, err := c.conn.Write(reqBuf.Bytes()); err != nil {
		return nil, err
	}

	return c.readResponse(id)
}

// readResponse читает ответ от сервера
func (c *TCPClient) readResponse(expectedID uint32) ([]byte, error) {
	c.muReader.Lock()
	defer c.muReader.Unlock()

	// Читаем длину сообщения (4 байта)
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(c.conn, lenBuf); err != nil {
		return nil, err
	}
	msgLen := binary.BigEndian.Uint32(lenBuf)

	if msgLen > 10*1024*1024 {
		return nil, fmt.Errorf("ответ слишком большой")
	}

	// Читаем само сообщение
	msgBuf := make([]byte, msgLen)
	if _, err := io.ReadFull(c.conn, msgBuf); err != nil {
		return nil, err
	}

	// Десериализуем ответ
	var resp Response
	buf := bytes.NewReader(msgBuf)
	dec := gob.NewDecoder(buf)
	if err := dec.Decode(&resp); err != nil {
		return nil, err
	}

	if resp.ID != expectedID {
		return nil, fmt.Errorf("несовпадение ID")
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return resp.Result, nil
}

// ===============
// МЕТОДЫ КЛИЕНТА
// ===============

// OpenFile открывает образ FAT16
func (c *TCPClient) OpenFile(filename string) (*FAT16Data, error) {
	result, err := c.call("open", OpenParams{Filename: filename})
	if err != nil {
		return nil, err
	}
	var data FAT16Data
	buf := bytes.NewReader(result)
	if err := gob.NewDecoder(buf).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

// GetVisualization получает визуализацию
func (c *TCPClient) GetVisualization(filename string) (string, error) {
	result, err := c.call("visualize", VisualizeParams{Filename: filename})
	if err != nil {
		return "", err
	}
	var vis VisualizeResult
	buf := bytes.NewReader(result)
	if err := gob.NewDecoder(buf).Decode(&vis); err != nil {
		return "", err
	}
	return vis.Visualization, nil
}

// CreateDamage создает повреждение
func (c *TCPClient) CreateDamage(filename, damageType string) (*Damage, error) {
	result, err := c.call("create_damage", CreateDamageParams{
		Filename:   filename,
		DamageType: damageType,
	})
	if err != nil {
		return nil, err
	}
	var damage Damage
	buf := bytes.NewReader(result)
	if err := gob.NewDecoder(buf).Decode(&damage); err != nil {
		return nil, err
	}
	return &damage, nil
}

// CheckFileSystem проверяет и исправляет ФС
func (c *TCPClient) CheckFileSystem(filename string) (*CheckResult, []Damage, error) {
	result, err := c.call("check", CheckParams{Filename: filename})
	if err != nil {
		return nil, nil, err
	}
	var response CheckResponse
	buf := bytes.NewReader(result)
	if err := gob.NewDecoder(buf).Decode(&response); err != nil {
		return nil, nil, err
	}
	return &response.Result, response.Fixes, nil
}

// CloseFile закрывает файл
func (c *TCPClient) CloseFile(filename string) error {
	_, err := c.call("close", CloseParams{Filename: filename})
	return err
}

// ========================
// СТРУКТУРЫ ДЛЯ ПРОТОКОЛА
// ========================

type OpenParams struct {
	Filename string
}
type VisualizeParams struct {
	Filename string
}
type VisualizeResult struct {
	Visualization string
}
type CreateDamageParams struct {
	Filename   string
	DamageType string
}
type CheckParams struct {
	Filename string
}
type CloseParams struct {
	Filename string
}
type Request struct {
	Method string
	ID     uint32
	Params []byte
}
type Response struct {
	ID     uint32
	Result []byte
	Error  string
}
type CheckResponse struct {
	Result CheckResult
	Fixes  []Damage
}

func init() {
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

// ====
// GUI
// ====

type GUI struct {
	app           fyne.App
	window        fyne.Window
	client        *TCPClient
	status        *widget.Label
	currentFile   string
	visualization *widget.Label
	logList       *widget.List
	logEntries    []LogEntry
	nextLogID     int
	portEntry     *widget.Entry
	connectBtn    *widget.Button
}

// NewGUI создает новый GUI
func NewGUI(a fyne.App, w fyne.Window) *GUI {
	gui := &GUI{
		app:        a,
		window:     w,
		logEntries: make([]LogEntry, 0),
		nextLogID:  1,
	}
	gui.createUI()
	return gui
}

func (g *GUI) addLog(logType, details string) {
	entry := LogEntry{
		ID:        g.nextLogID,
		Timestamp: time.Now(),
		Type:      logType,
		Details:   details,
	}
	g.nextLogID++
	g.logEntries = append(g.logEntries, entry)

	if len(g.logEntries) > 200 {
		g.logEntries = g.logEntries[len(g.logEntries)-200:]
		for i := range g.logEntries {
			g.logEntries[i].ID = i + 1
		}
		g.nextLogID = len(g.logEntries) + 1
	}

	if g.logList != nil {
		g.logList.Refresh()
	}
}

func (g *GUI) addErrorLog(details string)  { g.addLog("❌ ОШИБКА", details) }
func (g *GUI) addFixLog(details string)    { g.addLog("✅ ИСПРАВЛЕНИЕ", details) }
func (g *GUI) addInfoLog(details string)   { g.addLog("ℹ️ ИНФО", details) }
func (g *GUI) addDamageLog(details string) { g.addLog("⚠️ ПОВРЕЖДЕНИЕ", details) }

func (g *GUI) formatLogEntry(entry LogEntry) string {
	timeStr := entry.Timestamp.Format("15:04:05")
	idStr := fmt.Sprintf("%3d", entry.ID)
	return fmt.Sprintf("%s | %s | %-12s | %s", timeStr, idStr, entry.Type, entry.Details)
}

func (g *GUI) createUI() {
	g.status = widget.NewLabel("Не подключено")
	g.visualization = widget.NewLabel("")
	g.visualization.Wrapping = fyne.TextWrapWord

	g.logList = widget.NewList(
		func() int { return len(g.logEntries) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			label := o.(*widget.Label)
			label.SetText(g.formatLogEntry(g.logEntries[i]))
			label.Importance = widget.MediumImportance
		},
	)

	g.portEntry = widget.NewEntry()
	g.portEntry.SetText("8080")
	g.connectBtn = widget.NewButton("🔌 Подключиться", g.connectToServer)

	openBtn := widget.NewButton("📂 Открыть образ", g.openFile)
	visualizeBtn := widget.NewButton("👁️ Визуализация", g.updateVisualization)
	refreshBtn := widget.NewButton("🔄 Обновить", g.updateVisualization)
	clearLogBtn := widget.NewButton("🧹 Очистить", func() {
		g.logEntries = make([]LogEntry, 0)
		g.nextLogID = 1
		g.logList.Refresh()
		g.addInfoLog("Лог очищен")
	})

	missingBtn := widget.NewButton("❌ Отсутствие EOF", func() { g.createDamage("missing_eof") })
	intersectionBtn := widget.NewButton("↔️ Пересечение", func() { g.createDamage("intersection") })
	loopBtn := widget.NewButton("🔁 Зацикленность", func() { g.createDamage("loop") })
	checkBtn := widget.NewButton("🔍 Проверить и исправить", g.checkFS)

	controlPanel := container.NewVBox(
		widget.NewLabelWithStyle("🔌 ПОДКЛЮЧЕНИЕ", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		container.NewBorder(nil, nil, widget.NewLabel("Порт:"), g.connectBtn, g.portEntry),
		widget.NewSeparator(),
		openBtn,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("👁️ ВИЗУАЛИЗАЦИЯ", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		container.NewGridWithColumns(2, visualizeBtn, refreshBtn),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("⚠️ ПОВРЕЖДЕНИЯ", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		container.NewGridWithColumns(3, missingBtn, intersectionBtn, loopBtn),
		widget.NewSeparator(),
		checkBtn,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("📋 СТАТУС", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		g.status,
	)

	visualizationScroll := container.NewScroll(g.visualization)
	visualizationScroll.SetMinSize(fyne.NewSize(600, 350))

	logScroll := container.NewScroll(g.logList)
	logScroll.SetMinSize(fyne.NewSize(600, 250))

	logHeader := container.NewHBox(
		widget.NewLabelWithStyle("📋 ЖУРНАЛ ОПЕРАЦИЙ", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("(Время | № | Тип | Действие)"),
		clearLogBtn,
	)

	rightPanel := container.NewBorder(
		widget.NewLabelWithStyle("ВИЗУАЛИЗАЦИЯ FAT16", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		container.NewBorder(logHeader, nil, nil, nil, logScroll),
		nil, nil,
		visualizationScroll,
	)

	split := container.NewHSplit(controlPanel, rightPanel)
	split.SetOffset(0.28)
	g.window.SetContent(split)
}

func (g *GUI) connectToServer() {
	port := g.portEntry.Text
	if port == "" {
		dialog.ShowError(fmt.Errorf("введите порт"), g.window)
		return
	}

	g.connectBtn.Disable()
	g.connectBtn.SetText("Подключение...")
	g.status.SetText(fmt.Sprintf("Подключение к порту %s...", port))

	go func() {
		client, err := NewTCPClient(port)
		if err != nil {
			g.connectBtn.Enable()
			g.connectBtn.SetText("🔌 Подключиться")
			g.status.SetText(fmt.Sprintf("Ошибка: %v", err))
			g.addErrorLog(fmt.Sprintf("Не удалось подключиться к порту %s", port))
			dialog.ShowError(fmt.Errorf("ошибка: %v", err), g.window)
			return
		}
		g.client = client
		g.connectBtn.Enable()
		g.connectBtn.SetText("🔌 Подключиться")
		g.status.SetText(fmt.Sprintf("Подключено к порту %s", port))
		g.addInfoLog(fmt.Sprintf("Подключено к серверу на порту %s", port))
	}()
}

func (g *GUI) openFile() {
	if g.client == nil {
		dialog.ShowError(fmt.Errorf("сначала подключитесь"), g.window)
		return
	}

	dialog.ShowFileOpen(func(file fyne.URIReadCloser, err error) {
		if err != nil || file == nil {
			return
		}
		defer file.Close()

		g.currentFile = file.URI().Path()
		g.addInfoLog(fmt.Sprintf("Выбран файл: %s", g.currentFile))

		data, err := g.client.OpenFile(g.currentFile)
		if err != nil {
			g.addErrorLog(fmt.Sprintf("Ошибка: %v", err))
			dialog.ShowError(fmt.Errorf("ошибка: %v", err), g.window)
			g.currentFile = ""
			return
		}

		g.status.SetText(fmt.Sprintf("Открыт: %s (%d файлов)", g.currentFile, len(data.Files)))
		g.addInfoLog(fmt.Sprintf("Файл открыт: %d файлов, %d кластеров", len(data.Files), len(data.FatTable)))
		g.updateVisualization()
	}, g.window)
}

func (g *GUI) updateVisualization() {
	if g.client == nil || g.currentFile == "" {
		return
	}

	g.status.SetText("Загрузка...")
	vis, err := g.client.GetVisualization(g.currentFile)
	if err != nil {
		g.addErrorLog(fmt.Sprintf("Ошибка: %v", err))
		g.visualization.SetText(fmt.Sprintf("Ошибка:\n%v", err))
		g.status.SetText("Ошибка")
		return
	}

	g.visualization.SetText(vis)
	g.status.SetText("Готово")
	g.addInfoLog("Визуализация обновлена")
}

func (g *GUI) createDamage(damageType string) {
	if g.client == nil || g.currentFile == "" {
		dialog.ShowError(fmt.Errorf("сначала подключитесь и откройте файл"), g.window)
		return
	}

	names := map[string]string{
		"missing_eof":  "отсутствие EOF",
		"intersection": "пересечение",
		"loop":         "зацикленность",
	}

	g.status.SetText(fmt.Sprintf("Создание %s...", names[damageType]))
	damage, err := g.client.CreateDamage(g.currentFile, damageType)
	if err != nil {
		g.addErrorLog(fmt.Sprintf("Ошибка: %v", err))
		dialog.ShowError(fmt.Errorf("ошибка: %v", err), g.window)
		g.status.SetText("Ошибка")
		return
	}

	g.status.SetText("Готово")
	g.addDamageLog(damage.Description)
	dialog.ShowInformation("Успешно", damage.Description, g.window)
	g.updateVisualization()
}

func (g *GUI) checkFS() {
	if g.client == nil || g.currentFile == "" {
		dialog.ShowError(fmt.Errorf("сначала подключитесь и откройте файл"), g.window)
		return
	}

	g.status.SetText("Проверка ФС...")
	result, fixes, err := g.client.CheckFileSystem(g.currentFile)
	if err != nil {
		g.addErrorLog(fmt.Sprintf("Ошибка: %v", err))
		dialog.ShowError(fmt.Errorf("ошибка: %v", err), g.window)
		g.status.SetText("Ошибка")
		return
	}

	// Логируем найденные ошибки
	for _, c := range result.MissingEOF {
		g.addLog("❌ ОШИБКА", fmt.Sprintf("Отсутствие EOF в кластере %d", c))
	}
	for _, inter := range result.Intersections {
		g.addLog("❌ ОШИБКА", fmt.Sprintf("Пересечение в кластере %d: %v", inter.Cluster, inter.Files))
	}
	for _, loop := range result.Loops {
		g.addLog("❌ ОШИБКА", fmt.Sprintf("Зацикленность: %s (кластер %d)", loop.FileName, loop.StartCluster))
	}

	// Логируем исправления
	for _, fix := range fixes {
		g.addFixLog(fix.Description)
	}

	// Показываем диалог с результатом
	errorCount := len(result.MissingEOF) + len(result.Intersections) + len(result.Loops)
	fixCount := len(fixes)

	message := fmt.Sprintf("Найдено ошибок: %d\nИсправлено: %d", errorCount, fixCount)
	if fixCount > 0 {
		message += "\n\nИсправления:\n"
		for i, fix := range fixes {
			if i < 5 {
				message += fmt.Sprintf("• %s\n", fix.Description)
			}
		}
		if len(fixes) > 5 {
			message += fmt.Sprintf("... и еще %d исправлений", len(fixes)-5)
		}
	}

	dialog.ShowInformation("Результат проверки", message, g.window)

	if fixCount > 0 {
		g.status.SetText(fmt.Sprintf("Исправлено: %d", fixCount))
		g.addInfoLog(fmt.Sprintf("Исправлено %d ошибок", fixCount))
	} else {
		g.status.SetText("Ошибок не найдено")
	}
	g.updateVisualization()
}

func (g *GUI) Run() {
	g.window.SetOnClosed(func() {
		if g.client != nil {
			if g.currentFile != "" {
				g.client.CloseFile(g.currentFile)
			}
			g.client.Close()
		}
	})
	g.window.Resize(fyne.NewSize(1100, 700))
	g.window.ShowAndRun()
}
