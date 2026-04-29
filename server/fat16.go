package main

/*
#cgo CFLAGS: -I..
#cgo LDFLAGS: -L.. -lfat16
#include "fat16.h"
#include <stdlib.h>
*/
import "C"
import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// FAT16Handler обработчик FAT16 через C библиотеку
type FAT16Handler struct {
	mu           sync.RWMutex
	filename     string
	handle       int
	lastModified time.Time
}

// NewFAT16Handler создает новый обработчик
func NewFAT16Handler(filename string) (*FAT16Handler, error) {
	cFilename := C.CString(filename)
	defer C.free(unsafe.Pointer(cFilename))

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

// GetData получает данные FAT16
func (h *FAT16Handler) GetData() (*FAT16Data, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	filesBuf := make([]byte, 1024*1024)
	size := C.GetFiles(C.int(h.handle), (*C.char)(unsafe.Pointer(&filesBuf[0])), C.int(len(filesBuf)))
	if size < 0 {
		return nil, fmt.Errorf("ошибка получения файлов: %d", size)
	}

	var files []FileInfo
	if err := json.Unmarshal(filesBuf[:size], &files); err != nil {
		return nil, fmt.Errorf("ошибка парсинга файлов: %v", err)
	}

	fatBuf := make([]byte, 1024*1024)
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

// GetVisualization получает текстовую визуализацию
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
				return ChainResult{Chain: chain, HasLoop: true, LoopAt: pos}
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

	for _, f := range data.Files {
		emoji := "📄"
		if f.IsDirectory {
			emoji = "📂"
		}

		sizeStr := fmt.Sprintf("%d байт", f.Size)
		if f.Size > 1024 {
			sizeStr = fmt.Sprintf("%.1f KB", float64(f.Size)/1024)
		}
		if f.Size > 1024*1024 {
			sizeStr = fmt.Sprintf("%.1f MB", float64(f.Size)/(1024*1024))
		}

		sb.WriteString(fmt.Sprintf("%s %s (%s)\n", emoji, f.Name, sizeStr))
		sb.WriteString(fmt.Sprintf("   📍 Стартовый кластер: %d\n", f.StartCluster))

		chainResult := getChainWithLoopDetection(f.StartCluster)

		if chainResult.HasLoop {
			sb.WriteString("   🔁 ⚠️ ОБНАРУЖЕН ЦИКЛ! ⚠️\n")
			sb.WriteString("   🔗 Цепочка до цикла: ")
			for i, c := range chainResult.Chain {
				if i > 0 {
					sb.WriteString(" → ")
				}
				sb.WriteString(fmt.Sprintf("%d", c))
			}
			sb.WriteString("\n")
			sb.WriteString(fmt.Sprintf("   🔄 Зацикливание: кластер %d -> уже посещенный кластер %d\n",
				chainResult.Chain[len(chainResult.Chain)-1], chainResult.Chain[chainResult.LoopAt]))
			sb.WriteString("   💡 Для исправления нажмите 'Проверить и исправить'\n")
		} else if len(chainResult.Chain) > 0 {
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

// CreateDamage создает повреждение
func (h *FAT16Handler) CreateDamage(damageType string) (*Damage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	cDamageType := C.CString(damageType)
	defer C.free(unsafe.Pointer(cDamageType))

	resultBuf := make([]byte, 64*1024)
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

	resultBuf := make([]byte, 1024*1024)
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

// Close закрывает обработчик
func (h *FAT16Handler) Close() {
	C.CloseFAT16(C.int(h.handle))
}
