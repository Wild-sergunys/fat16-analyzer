package main

import (
	"encoding/gob"
	"time"
)

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

// Damage повреждение
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
	Method string `json:"method"`
	ID     uint32 `json:"id"`
	Params []byte `json:"params"`
}

// Response ответ сервера
type Response struct {
	ID     uint32 `json:"id"`
	Result []byte `json:"result"`
	Error  string `json:"error"`
}

// OpenParams параметры для открытия файла
type OpenParams struct {
	Filename string
}

// VisualizeParams параметры для визуализации
type VisualizeParams struct {
	Filename string
}

// VisualizeResult результат визуализации
type VisualizeResult struct {
	Visualization string
}

// CreateDamageParams параметры для создания повреждения
type CreateDamageParams struct {
	Filename   string
	DamageType string
}

// CheckParams параметры для проверки ФС
type CheckParams struct {
	Filename string
}

// CloseParams параметры для закрытия файла
type CloseParams struct {
	Filename string
}

// CheckResponse результат проверки с исправлениями
type CheckResponse struct {
	Result CheckResult
	Fixes  []Damage
}

// Константы
const (
	MAX_MSG_SIZE   = 10 * 1024 * 1024
	READ_TIMEOUT   = 20 * time.Minute
	MAX_OPEN_FILES = 100
)

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
